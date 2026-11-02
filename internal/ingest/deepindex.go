package ingest

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/libtype"
)

// deepIndexBatchSize is the number of books the writer commits per transaction
// (and per write-guard acquisition).
const deepIndexBatchSize = 500

// DeepIndexScope selects which unchecked books of a library a deep index run
// visits. Build one with DeepIndexAll or DeepIndexImportedAt.
type DeepIndexScope struct {
	importedAt sql.NullInt64
}

// DeepIndexAll visits every unchecked book of the library (the "Deep Index"
// button).
func DeepIndexAll() DeepIndexScope { return DeepIndexScope{} }

// DeepIndexImportedAt visits only the unchecked books stamped by the sync run
// whose imported_at is stamp (the post-sync fill for newly imported books).
func DeepIndexImportedAt(stamp int64) DeepIndexScope {
	return DeepIndexScope{importedAt: sql.NullInt64{Int64: stamp, Valid: true}}
}

// DeepIndexResult counts the outcome of a deep index run. Every book the run
// committed lands in exactly one of Checked or Errors; Filled and Skipped are
// subsets of Checked. A book whose batch was not committed (the run was
// cancelled or failed first) is counted nowhere and stays unchecked.
type DeepIndexResult struct {
	// Checked is the number of books marked metadata_checked: those whose file
	// was read (with or without an annotation in it) plus PDF-only books, which
	// are marked without being parsed.
	Checked int
	// Filled is the number of checked books whose annotation was NULL and now
	// holds the annotation recovered from their file.
	Filled int
	// Skipped is the number of checked books that already had an annotation,
	// which was kept as is.
	Skipped int
	// Errors is the number of books left unchecked for a retry on a later run:
	// unreadable archive or file, missing entry, or a parse error.
	Errors int
}

// DeepIndexer fills missing book descriptions (and gap-fills identifiers) from
// the books' own local files and makes them full-text searchable. INPX
// libraries are read archive by archive with a header-only FB2 parse; other
// library types go per book through the local FileExtractor. It never makes
// network calls and never writes to a library source.
type DeepIndexer struct {
	log       *slog.Logger
	db        *sql.DB
	guard     *db.WriteGuard
	extractor FileExtractor

	// batchSize and parseFB2 are fixed in production; tests override them.
	batchSize int
	parseFB2  func(io.Reader) (ebook.Metadata, error)
}

// NewDeepIndexer builds a DeepIndexer. extractor is the local-file tier used for
// non-INPX libraries (and non-FB2 entries of an INPX library).
func NewDeepIndexer(
	log *slog.Logger, database *sql.DB, guard *db.WriteGuard, extractor FileExtractor,
) *DeepIndexer {
	return &DeepIndexer{
		log:       log,
		db:        database,
		guard:     guard,
		extractor: extractor,
		batchSize: deepIndexBatchSize,
		parseFB2:  ebook.ParseFB2Description,
	}
}

// deepIndexJob is one unit of worker input: all wanted entries of one INPX
// archive, or a single book of another library type.
type deepIndexJob struct {
	archivePath string // INPX only; empty for per-book jobs
	items       []dbq.ListDeepIndexWorkRow
}

// deepIndexOutcome is a worker's verdict for one book.
type deepIndexOutcome struct {
	bookID int64
	meta   ebook.Metadata
	err    error // non-nil: leave the book unchecked
}

// Run indexes the library's unchecked books in scope. It reports the number of
// books in scope via rep.SetTotal and each committed batch via rep.Add. On
// cancellation it returns the counts of the batches committed so far together
// with ctx's error; those batches stay committed and the remaining books are
// picked up by the next run.
func (d *DeepIndexer) Run(
	ctx context.Context, library dbq.Library, scope DeepIndexScope, rep Reporter,
) (DeepIndexResult, error) {
	start := time.Now()

	work, err := dbq.New(d.db).ListDeepIndexWork(ctx, dbq.ListDeepIndexWorkParams{
		LibraryID: library.ID, ImportedAt: scope.importedAt,
	})
	if err != nil {
		return DeepIndexResult{}, fmt.Errorf("list deep index work: %w", err)
	}
	rep.SetTotal(len(work))

	res, err := d.process(ctx, library, d.jobs(library, work), rep)

	d.log.Info("deep index finished",
		slog.Int64("library_id", library.ID),
		slog.Int("checked", res.Checked),
		slog.Int("filled", res.Filled),
		slog.Int("skipped", res.Skipped),
		slog.Int("errors", res.Errors),
		slog.Duration("duration", time.Since(start)),
		slog.Bool("complete", err == nil),
	)

	return res, err
}

// jobs groups the work list: one job per archive for INPX (the list is ordered
// by source_path, so an archive's entries are contiguous), one job per book
// otherwise.
func (d *DeepIndexer) jobs(library dbq.Library, work []dbq.ListDeepIndexWorkRow) []deepIndexJob {
	if library.Type != libtype.INPX {
		jobs := make([]deepIndexJob, len(work))
		for i := range work {
			jobs[i] = deepIndexJob{items: work[i : i+1]}
		}

		return jobs
	}

	var jobs []deepIndexJob
	byArchive := map[string]int{}
	for _, row := range work {
		archiveName, _, _ := strings.Cut(row.SourcePath, "/")
		path := filepath.Join(filepath.Dir(library.Path), archiveName)
		i, ok := byArchive[path]
		if !ok {
			i = len(jobs)
			byArchive[path] = i
			jobs = append(jobs, deepIndexJob{archivePath: path})
		}
		jobs[i].items = append(jobs[i].items, row)
	}

	return jobs
}

// process runs the bounded worker pool and the single writer (the calling
// goroutine). It returns only after every worker has exited.
func (d *DeepIndexer) process(
	ctx context.Context, library dbq.Library, jobs []deepIndexJob, rep Reporter,
) (DeepIndexResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobCh := make(chan deepIndexJob)
	results := make(chan deepIndexOutcome, d.batchSize)

	go func() {
		defer close(jobCh)
		for _, job := range jobs {
			select {
			case jobCh <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for range max(1, min(runtime.GOMAXPROCS(0), len(jobs))) {
		wg.Go(func() {
			for job := range jobCh {
				d.runJob(ctx, library, job, results)
			}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	res, err := d.write(ctx, results, rep)

	// Stop the producer and workers (their sends select on ctx), then drain so
	// none is left blocked and no archive is still open when Run returns.
	cancel()
	for range results {
	}

	return res, err
}

// runJob produces one outcome per item of the job. Every send selects on ctx so
// a worker never blocks once the writer has stopped.
func (d *DeepIndexer) runJob(
	ctx context.Context, library dbq.Library, job deepIndexJob, out chan<- deepIndexOutcome,
) {
	send := func(o deepIndexOutcome) bool {
		select {
		case out <- o:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if library.Type != libtype.INPX {
		for _, item := range job.items {
			if ctx.Err() != nil || !send(d.extract(ctx, item)) {
				return
			}
		}

		return
	}

	a := &deepIndexArchive{path: job.archivePath}
	defer a.close()
	for _, item := range job.items {
		if ctx.Err() != nil || !send(d.inpxItem(ctx, a, item)) {
			return
		}
	}
}

// deepIndexArchive opens an INPX archive lazily, at most once per job, so an
// archive holding only PDF entries is never opened.
type deepIndexArchive struct {
	path string
	zr   *zip.ReadCloser
	err  error
}

func (a *deepIndexArchive) reader() (*zip.ReadCloser, error) {
	if a.zr == nil && a.err == nil {
		a.zr, a.err = zip.OpenReader(a.path)
	}

	return a.zr, a.err
}

func (a *deepIndexArchive) close() {
	if a.zr != nil {
		_ = a.zr.Close()
	}
}

// inpxItem resolves one book of an INPX archive job.
func (d *DeepIndexer) inpxItem(
	ctx context.Context, a *deepIndexArchive, item dbq.ListDeepIndexWorkRow,
) deepIndexOutcome {
	switch {
	case isPDF(item.FileFormat):
		return deepIndexOutcome{bookID: item.BookID}
	case !strings.EqualFold(item.FileFormat, ebook.FormatFB2):
		return d.extract(ctx, item) // epub/mobi inside an INPX archive: full local parse
	}
	zr, err := a.reader()
	if err != nil {
		return deepIndexOutcome{bookID: item.BookID, err: fmt.Errorf("open archive: %w", err)}
	}

	return d.parseEntry(zr, item)
}

// parseEntry header-parses one FB2 entry of an open INPX archive.
func (d *DeepIndexer) parseEntry(zr *zip.ReadCloser, item dbq.ListDeepIndexWorkRow) deepIndexOutcome {
	_, inner, found := strings.Cut(item.SourcePath, "/")
	if !found {
		return deepIndexOutcome{bookID: item.BookID, err: fmt.Errorf("malformed source path %q", item.SourcePath)}
	}
	rc, err := zr.Open(inner)
	if err != nil {
		return deepIndexOutcome{bookID: item.BookID, err: fmt.Errorf("open entry %s: %w", inner, err)}
	}
	defer func() { _ = rc.Close() }()

	meta, err := d.parseFB2(rc)
	if err != nil {
		return deepIndexOutcome{bookID: item.BookID, err: fmt.Errorf("parse entry %s: %w", inner, err)}
	}
	meta.Identifiers = cleanedEbookIdentifiers(meta.Identifiers)

	return deepIndexOutcome{bookID: item.BookID, meta: meta}
}

// extract reads one book through the local FileExtractor. PDF-only books are
// marked checked without being read.
func (d *DeepIndexer) extract(ctx context.Context, item dbq.ListDeepIndexWorkRow) deepIndexOutcome {
	if isPDF(item.FileFormat) {
		return deepIndexOutcome{bookID: item.BookID}
	}
	meta, ok, err := d.extractor.Backfill(ctx, item.BookID)
	if err != nil {
		return deepIndexOutcome{bookID: item.BookID, err: fmt.Errorf("extract: %w", err)}
	}
	if !ok {
		meta = ebook.Metadata{}
	}

	return deepIndexOutcome{bookID: item.BookID, meta: meta}
}

func isPDF(format string) bool { return strings.EqualFold(format, ebook.FormatPDF) }

// write is the single writer: it collects outcomes into batches and commits
// each under its own write-guard acquisition.
func (d *DeepIndexer) write(
	ctx context.Context, results <-chan deepIndexOutcome, rep Reporter,
) (DeepIndexResult, error) {
	var res DeepIndexResult
	batch := make([]deepIndexOutcome, 0, d.batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // expose the context cause to the caller
		}
		tally, err := d.commit(ctx, batch)
		if err != nil {
			return err
		}
		res.Checked += tally.Checked
		res.Filled += tally.Filled
		res.Skipped += tally.Skipped
		res.Errors += tally.Errors
		rep.Add(len(batch))
		batch = batch[:0]

		return nil
	}

	for {
		select {
		case o, ok := <-results:
			if !ok {
				return res, flush()
			}
			batch = append(batch, o)
			if len(batch) >= d.batchSize {
				if err := flush(); err != nil {
					return res, err
				}
			}
		case <-ctx.Done():
			return res, ctx.Err() //nolint:wrapcheck // expose the context cause to the caller
		}
	}
}

// commit writes one batch in a single transaction under the write guard and
// returns its tally, which only counts once the transaction has committed.
func (d *DeepIndexer) commit(ctx context.Context, batch []deepIndexOutcome) (DeepIndexResult, error) {
	var tally DeepIndexResult
	err := d.guard.WithTx(ctx, d.db, func(tx *sql.Tx) error {
		tally = DeepIndexResult{}
		q := dbq.New(tx)
		for i := range batch {
			o := &batch[i]
			if o.err != nil {
				d.log.Debug("deep index: book left unchecked",
					slog.Int64("book", o.bookID), slog.Any("error", o.err))
				tally.Errors++

				continue
			}
			filled, skipped, err := d.persist(ctx, tx, q, o)
			if errors.Is(err, sql.ErrNoRows) {
				continue // book deleted since the work list was read
			}
			if err != nil {
				return fmt.Errorf("book %d: %w", o.bookID, err)
			}
			tally.Checked++
			if filled {
				tally.Filled++
			}
			if skipped {
				tally.Skipped++
			}
		}

		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DeepIndexResult{}, ctxErr //nolint:wrapcheck // expose the context cause to the caller
		}

		return DeepIndexResult{}, fmt.Errorf("commit deep index batch: %w", err)
	}

	return tally, nil
}

// persist stores one book's outcome: the annotation only when the book has none
// (plus its FTS row), identifiers insert-if-absent, then the checked mark.
func (d *DeepIndexer) persist(
	ctx context.Context, tx *sql.Tx, q *dbq.Queries, o *deepIndexOutcome,
) (filled, skipped bool, err error) {
	has, err := q.BookHasAnnotation(ctx, o.bookID)
	if err != nil {
		return false, false, fmt.Errorf("probe annotation: %w", err)
	}

	switch {
	case has:
		skipped = true
	case strings.TrimSpace(o.meta.Annotation) != "":
		n, err := q.SetBookAnnotationIfNull(ctx, dbq.SetBookAnnotationIfNullParams{
			Annotation: sql.NullString{String: o.meta.Annotation, Valid: true}, ID: o.bookID,
		})
		if err != nil {
			return false, false, fmt.Errorf("set annotation: %w", err)
		}
		if n > 0 {
			if err := db.RefreshBookFTS(ctx, tx, o.bookID); err != nil {
				return false, false, fmt.Errorf("refresh fts: %w", err)
			}
			filled = true
		}
	}

	for _, id := range o.meta.Identifiers {
		err := q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
			BookID: o.bookID, Type: id.Type, Value: id.Value,
		})
		if err != nil {
			return false, false, fmt.Errorf("insert identifier: %w", err)
		}
	}
	if err := q.MarkMetadataChecked(ctx, o.bookID); err != nil {
		return false, false, fmt.Errorf("mark checked: %w", err)
	}

	return filled, skipped, nil
}
