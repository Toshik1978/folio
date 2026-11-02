package ingest

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// bookReader is the subset of *ebook.Dispatcher the ingest package needs:
// Source-based metadata and cover reads, the per-format rank and description
// traits, and the extension support probes. Defined at the consumer so tests
// can inject a fake.
type bookReader interface {
	Supported(name string) bool
	Format(name string) string
	Rank(format string) int
	Describes(format string) bool
	Metadata(ctx context.Context, log *slog.Logger, src ebook.Source) (ebook.Metadata, error)
	Cover(ctx context.Context, log *slog.Logger, src ebook.Source) ([]byte, error)
}

// bookContent is what readBook recovered from a book file: meta for
// flowMetadata, cover for flowCover.
type bookContent struct {
	meta  ebook.Metadata
	cover []byte
}

// Extractor lazily recovers cover images and descriptions from a book's library
// files (a standalone file, or an entry of an archive read in memory). It is
// used by the API for on-demand extraction.
// Library files are only ever read, and nothing is written to disk.
type Extractor struct {
	db     *sql.DB
	log    *slog.Logger
	reader bookReader
	guard  *db.WriteGuard
	res    resolver

	// onArchiveOpen, when set, is installed as the onOpen hook of every session's
	// archive cache; tests count archive opens through it.
	onArchiveOpen func(path string)
}

// NewExtractor returns an Extractor reading from the folio database. guard is
// the process-wide write guard serializing its cover-flow record writes.
func NewExtractor(
	database *sql.DB,
	log *slog.Logger,
	reader bookReader, //nolint:iface // tests inject a fake reader
	guard *db.WriteGuard,
) *Extractor {
	return &Extractor{db: database, log: log, reader: reader, guard: guard, res: newResolver()}
}

// Cover returns the book's cover image bytes; ok is false when none exists.
// The read is gated and recorded under the cover flow: a done read deletes the
// book's stale cover record (touching the write guard only when one was
// stored), a newly broken one stores it (logged once), and both a
// newly and a known broken read return an error wrapping ebook.ErrMalformed,
// the known one without opening any file. A retryable read returns its error
// and records nothing. Record writes are best-effort under the write guard.
func (e *Extractor) Cover(ctx context.Context, bookID int64) ([]byte, bool, error) {
	got, res := e.read(ctx, flowCover, bookID)
	switch res.Status {
	case ReadDone:
		if res.Recorded {
			clearReadFailure(ctx, e.log, e.guard, dbq.New(e.db), bookID, flowCover)
		}
	case ReadBroken:
		if !res.Known {
			recordReadFailure(ctx, e.log, e.guard, dbq.New(e.db), bookID, flowCover, res)
		}
		if errors.Is(res.Err, ebook.ErrMalformed) {
			return nil, false, res.Err
		}

		return nil, false, fmt.Errorf("%w: %w", ebook.ErrMalformed, res.Err)
	case ReadRetryable:
		return nil, false, res.Err
	}
	if len(got.cover) == 0 {
		return nil, false, nil
	}

	return got.cover, true, nil
}

// Description returns the full metadata read from the book's source file
// (annotation, identifiers, ...), with identifiers cleaned and deduplicated to
// match what sync persists, and the outcome of the read. Formats that are not
// read for descriptions (PDF) are skipped, so a book with only such files is
// done with empty metadata without being opened.
func (e *Extractor) Description(ctx context.Context, bookID int64) (ebook.Metadata, ReadResult) {
	library, files, err := e.load(ctx, bookID)
	if err != nil {
		return ebook.Metadata{}, classify(err)
	}
	sess := e.newSession()
	defer sess.close()

	return sess.description(ctx, library, files, e.storedRecord)
}

// read loads the book, its library and its files, then runs readBook in a
// one-shot session. A book that is gone, or none of whose files is a candidate
// for flow, is done with no content.
func (e *Extractor) read(ctx context.Context, flow readFlow, bookID int64) (bookContent, ReadResult) {
	library, files, err := e.load(ctx, bookID)
	if err != nil {
		return bookContent{}, classify(err)
	}
	sess := e.newSession()
	defer sess.close()

	return e.readBook(ctx, flow, library, files, sess.ac)
}

// load returns the book's library and files. A book that is gone has no
// files, so a read of it finds no candidate and returns ok=false.
func (e *Extractor) load(ctx context.Context, bookID int64) (dbq.Library, []dbq.BookFile, error) {
	q := dbq.New(e.db)

	book, err := q.GetBook(ctx, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return dbq.Library{}, nil, nil
	}
	if err != nil {
		return dbq.Library{}, nil, fmt.Errorf("get book: %w", err)
	}
	library, err := q.GetLibrary(ctx, book.LibraryID)
	if err != nil {
		return dbq.Library{}, nil, fmt.Errorf("get library: %w", err)
	}
	files, err := q.ListFilesForBook(ctx, bookID)
	if err != nil {
		return dbq.Library{}, nil, fmt.Errorf("list files: %w", err)
	}

	return library, files, nil
}

// readSession is a sequence of reads sharing one archive cache, so every
// archive is opened at most once for the session's lifetime. A Deep Index
// worker uses one session per job. It is not safe for concurrent use; close
// releases every archive it opened.
type readSession struct {
	e  *Extractor
	ac *archiveCache
}

// newSession returns a session owning a fresh archive cache.
func (e *Extractor) newSession() *readSession {
	ac := newArchiveCache()
	ac.onOpen = e.onArchiveOpen

	return &readSession{e: e, ac: ac}
}

// description reads the metadata of a book from the best of its files through
// readBookWith, gated by the record records returns, with identifiers cleaned
// like sync persists them. The metadata is empty unless the read is done.
func (s *readSession) description(
	ctx context.Context, library dbq.Library, files []dbq.BookFile, records recordSource,
) (ebook.Metadata, ReadResult) {
	got, res := s.e.readBookWith(ctx, flowMetadata, library, files, s.ac, records)
	if res.Status != ReadDone {
		return ebook.Metadata{}, res
	}
	got.meta.Identifiers = cleanedEbookIdentifiers(got.meta.Identifiers)

	return got.meta, res
}

// close releases every archive the session opened.
func (s *readSession) close() { s.ac.close() }

// errKnownBroken is the error of a read that the gate answered from an
// applicable broken-read record, without opening any file.
var errKnownBroken = errors.New("ingest: book recorded as unreadable")

// readRecord is the stored broken-read record of a book and flow, as the gate
// in readBook compares it.
type readRecord struct {
	reason        ebook.Reason
	detail        string
	fingerprint   string
	readerVersion int64
}

// recordSource returns the stored broken-read record of (bookID, flow), or nil
// when there is none. readBook queries the database through storedRecord; a
// caller that already holds the record (a Deep Index work row) supplies it.
type recordSource func(ctx context.Context, bookID int64, flow readFlow) (*readRecord, error)

// storedRecord loads the (bookID, flow) record from book_read_failures.
func (e *Extractor) storedRecord(ctx context.Context, bookID int64, flow readFlow) (*readRecord, error) {
	row, err := dbq.New(e.db).GetReadFailure(ctx, dbq.GetReadFailureParams{BookID: bookID, Flow: string(flow)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no record is a valid answer
	}
	if err != nil {
		return nil, fmt.Errorf("get read failure: %w", err)
	}

	return &readRecord{
		reason:        ebook.Reason(row.Reason),
		detail:        row.Detail,
		fingerprint:   row.Fingerprint,
		readerVersion: row.ReaderVersion,
	}, nil
}

// readBook reads flow from the best of a book's files, gated by the book's
// stored broken-read record. See readBookWith.
func (e *Extractor) readBook(
	ctx context.Context, flow readFlow, library dbq.Library, files []dbq.BookFile, ac *archiveCache,
) (bookContent, ReadResult) {
	return e.readBookWith(ctx, flow, library, files, ac, e.storedRecord)
}

// readBookWith reads flow from the best of a book's files. Candidates are
// ordered by the reader's rank, highest first (ties by file id); files the
// reader does not support are skipped, and so, for flowMetadata, are formats
// that are not read for descriptions. A book with no candidate is done with no
// content, and its record is not looked up.
//
// The gate comes first: when records holds a record for the book whose reader
// version is ebook.ReaderVersion and whose fingerprint equals the one
// recomputed from the candidates (stat or zip header, no entry inflated), the
// read is broken and Known, and no candidate is opened. Otherwise each
// candidate is read and classified in turn; the first done read is returned,
// else the results are combined. Once ctx is done no further candidate is
// opened and the read is retryable with ctx's error. The result carries the
// fingerprint, and Recorded when records held a record; a broken read whose
// fingerprint could not be computed is retryable instead, since it could not
// be recorded.
func (e *Extractor) readBookWith(
	ctx context.Context, flow readFlow, library dbq.Library, files []dbq.BookFile, ac *archiveCache,
	records recordSource,
) (bookContent, ReadResult) {
	cands := e.candidates(flow, files)
	if len(cands) == 0 {
		return bookContent{}, ReadResult{Status: ReadDone}
	}
	if err := ctx.Err(); err != nil {
		return bookContent{}, classify(err)
	}
	rec, err := records(ctx, cands[0].BookID, flow)
	if err != nil {
		return bookContent{}, classify(err)
	}
	fp, fpErr := e.res.fingerprint(library, cands, ac)
	if rec != nil && fpErr == nil && rec.readerVersion == ebook.ReaderVersion && rec.fingerprint == fp {
		return bookContent{}, ReadResult{
			Status:      ReadBroken,
			Reason:      rec.reason,
			Detail:      rec.detail,
			Fingerprint: fp,
			Known:       true,
			Recorded:    true,
			Err:         fmt.Errorf("%w: %s", errKnownBroken, rec.reason),
		}
	}

	got, res := e.readCandidates(ctx, flow, library, cands, ac)
	res.Fingerprint = fp
	if res.Status == ReadBroken && fpErr != nil {
		res = classify(fpErr)
	}
	res.Recorded = rec != nil

	return got, res
}

// readCandidates reads flow from cands in order until one read is done, and
// combines the classified failures otherwise.
func (e *Extractor) readCandidates(
	ctx context.Context, flow readFlow, library dbq.Library, cands []dbq.BookFile, ac *archiveCache,
) (bookContent, ReadResult) {
	results := make([]ReadResult, 0, len(cands))
	for _, file := range cands {
		if err := ctx.Err(); err != nil {
			return bookContent{}, classify(err)
		}
		got, err := e.readFile(ctx, flow, library, file, ac)
		if err == nil {
			return got, ReadResult{Status: ReadDone}
		}
		results = append(results, classify(err))
	}

	return bookContent{}, combine(results)
}

// candidates returns the files readBook may read for flow, best first.
func (e *Extractor) candidates(flow readFlow, files []dbq.BookFile) []dbq.BookFile {
	return rankCandidates(e.reader, flow, files)
}

// rankCandidates returns the files of a book that reader may read for flow,
// ordered by reader's rank, highest first (ties by file id). Files reader does
// not support are dropped, and so, for flowMetadata, are formats that are not
// read for descriptions. A broken-read record's fingerprint is computed over
// exactly this list.
func rankCandidates(reader bookReader, flow readFlow, files []dbq.BookFile) []dbq.BookFile {
	out := make([]dbq.BookFile, 0, len(files))
	for _, f := range files {
		if !reader.Supported(f.SourcePath) {
			continue
		}
		if flow == flowMetadata && !reader.Describes(f.FileFormat) {
			continue
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b dbq.BookFile) int {
		return cmp.Or(
			cmp.Compare(reader.Rank(b.FileFormat), reader.Rank(a.FileFormat)),
			cmp.Compare(a.ID, b.ID),
		)
	})

	return out
}

// readFile opens one book file through the resolver and reads flow from it.
func (e *Extractor) readFile(
	ctx context.Context, flow readFlow, library dbq.Library, file dbq.BookFile, ac *archiveCache,
) (bookContent, error) {
	src, closer, err := e.res.open(library, file, ac)
	if err != nil {
		return bookContent{}, err // already names the path; Detail is length-capped
	}
	defer func() { _ = closer.Close() }()

	var got bookContent
	if flow == flowCover {
		got.cover, err = e.reader.Cover(ctx, e.log, src)
	} else {
		got.meta, err = e.reader.Metadata(ctx, e.log, src)
	}
	if err != nil {
		return bookContent{}, fmt.Errorf("read %s %s: %w", flow, file.SourcePath, err)
	}

	return got, nil
}
