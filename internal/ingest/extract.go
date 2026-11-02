package ingest

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"

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

// readFlow names what a read of a book's files is for.
type readFlow string

const (
	// flowMetadata reads a book's description and identifiers.
	flowMetadata readFlow = "metadata"
	// flowCover reads a book's cover image.
	flowCover readFlow = "cover"
)

// bookContent is what readBook recovered from a book file: meta for
// flowMetadata, cover for flowCover.
type bookContent struct {
	meta  ebook.Metadata
	cover []byte
}

// Extractor lazily recovers cover images and descriptions from a book's library
// files (a standalone file, or an entry of an archive read in memory). It is
// used by the API for on-demand extraction and by the background cover-warming
// pass. Library files are only ever read, and nothing is written to disk.
type Extractor struct {
	db     *sql.DB
	log    *slog.Logger
	reader bookReader
	res    resolver

	// onArchiveOpen, when set, is installed as the onOpen hook of every session's
	// archive cache; tests count archive opens through it.
	onArchiveOpen func(path string)
}

// NewExtractor returns an Extractor reading from the folio database.
func NewExtractor(db *sql.DB, log *slog.Logger, reader bookReader) *Extractor { //nolint:iface
	return &Extractor{db: db, log: log, reader: reader, res: newResolver()}
}

// Cover returns the book's cover image bytes; ok is false when none exists.
func (e *Extractor) Cover(ctx context.Context, bookID int64) ([]byte, bool, error) {
	got, ok, err := e.read(ctx, flowCover, bookID)
	if err != nil || !ok || len(got.cover) == 0 {
		return nil, false, err
	}

	return got.cover, true, nil
}

// Description returns the full metadata read from the book's source file
// (annotation, identifiers, ...), with identifiers cleaned and deduplicated to
// match what sync persists. Formats that are not read for descriptions (PDF)
// are skipped, so a book with only such files returns ok=false without being
// opened.
func (e *Extractor) Description(ctx context.Context, bookID int64) (ebook.Metadata, bool, error) {
	library, files, err := e.load(ctx, bookID)
	if err != nil {
		return ebook.Metadata{}, false, err
	}
	sess := e.newSession()
	defer sess.close()

	return sess.description(ctx, library, files)
}

// read loads the book, its library and its files, then runs readBook in a
// one-shot session. ok is false when the book is gone or none of its files is
// a candidate for flow.
func (e *Extractor) read(ctx context.Context, flow readFlow, bookID int64) (bookContent, bool, error) {
	library, files, err := e.load(ctx, bookID)
	if err != nil {
		return bookContent{}, false, err
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
// readBook, with identifiers cleaned like sync persists them. ok is false when
// none of the files is read for descriptions.
func (s *readSession) description(
	ctx context.Context, library dbq.Library, files []dbq.BookFile,
) (ebook.Metadata, bool, error) {
	got, ok, err := s.e.readBook(ctx, flowMetadata, library, files, s.ac)
	if err != nil || !ok {
		return ebook.Metadata{}, false, err
	}
	got.meta.Identifiers = cleanedEbookIdentifiers(got.meta.Identifiers)

	return got.meta, true, nil
}

// close releases every archive the session opened.
func (s *readSession) close() { s.ac.close() }

// readBook reads flow from the best of a book's files. Candidates are ordered
// by the reader's rank, highest first (ties by file id); files the reader does
// not support are skipped, and so, for flowMetadata, are formats that are not
// read for descriptions. The first successful read is returned; when every
// candidate fails, the top candidate's error is. Once ctx is done no further
// candidate is opened and ctx's error is returned. ok is false when no file is
// a candidate.
func (e *Extractor) readBook(
	ctx context.Context, flow readFlow, library dbq.Library, files []dbq.BookFile, ac *archiveCache,
) (bookContent, bool, error) {
	var topErr error
	for _, file := range e.candidates(flow, files) {
		if err := ctx.Err(); err != nil {
			return bookContent{}, false, err //nolint:wrapcheck // expose the context cause to the caller
		}
		got, err := e.readFile(ctx, flow, library, file, ac)
		if err == nil {
			return got, true, nil
		}
		if topErr == nil {
			topErr = err
		}
	}
	if topErr != nil {
		return bookContent{}, false, topErr
	}

	return bookContent{}, false, nil
}

// candidates returns the files readBook may read for flow, best first.
func (e *Extractor) candidates(flow readFlow, files []dbq.BookFile) []dbq.BookFile {
	out := make([]dbq.BookFile, 0, len(files))
	for _, f := range files {
		if !e.reader.Supported(f.SourcePath) {
			continue
		}
		if flow == flowMetadata && !e.reader.Describes(f.FileFormat) {
			continue
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b dbq.BookFile) int {
		return cmp.Or(
			cmp.Compare(e.reader.Rank(b.FileFormat), e.reader.Rank(a.FileFormat)),
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
		return bookContent{}, fmt.Errorf("open %s: %w", file.SourcePath, err)
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
