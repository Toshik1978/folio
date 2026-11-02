package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// deepIndexBatchSize is the number of books the writer commits per transaction
// (and per write-guard acquisition).
const deepIndexBatchSize = 500

// deepIndexPageSize is the number of books a run loads from the work list at
// a time, so its memory stays flat however many books are unchecked (a
// 580k-book library held about 170 MB as one list).
const deepIndexPageSize = 5000

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
// committed lands in exactly one of Checked, Broken, KnownBroken or Errors;
// Filled and Skipped are subsets of Checked. A book whose batch was not committed (the run was
// cancelled or failed first) is counted nowhere and stays unchecked.
type DeepIndexResult struct {
	// Checked is the number of books marked metadata_checked: those whose file
	// was read (with or without an annotation in it) plus books with no file
	// read for descriptions (PDF-only, or no supported format), which are marked
	// without being read.
	Checked int
	// Filled is the number of checked books whose annotation was NULL and now
	// holds the annotation recovered from their file.
	Filled int
	// Skipped is the number of checked books that already had an annotation,
	// which was kept as is.
	Skipped int
	// Broken is the number of books whose files this run found unreadable for
	// good (see ReadBroken) and recorded in book_read_failures. They stay
	// unchecked.
	Broken int
	// KnownBroken is the number of books skipped without opening any entry
	// because their stored record still applies: same reader version, same
	// file fingerprint. They stay unchecked and nothing is written.
	KnownBroken int
	// Errors is the number of books left unchecked for a retry on a later run
	// because their read failed for a reason that may pass (ReadRetryable:
	// I/O, an unreadable archive, cancellation). Nothing is recorded for them.
	Errors int
}

// add folds the counts of part (a batch or a page) into r.
func (r *DeepIndexResult) add(part DeepIndexResult) {
	r.Checked += part.Checked
	r.Filled += part.Filled
	r.Skipped += part.Skipped
	r.Broken += part.Broken
	r.KnownBroken += part.KnownBroken
	r.Errors += part.Errors
}

// DeepIndexer fills missing book descriptions (and gap-fills identifiers) from
// the books' own local files and makes them full-text searchable. Every book is
// read through the Extractor's shared readBook, so the code names no format and
// no library type; books sharing an archive form one job whose session opens
// that archive once. It never makes network calls and never writes to a
// library source.
type DeepIndexer struct {
	log       *slog.Logger
	db        *sql.DB
	guard     *db.WriteGuard
	extractor *Extractor

	// batchSize and pageSize are fixed in production; tests lower them.
	batchSize int
	pageSize  int
}

// NewDeepIndexer builds a DeepIndexer reading books through extractor.
func NewDeepIndexer(log *slog.Logger, database *sql.DB, guard *db.WriteGuard, extractor *Extractor) *DeepIndexer {
	return &DeepIndexer{
		log:       log,
		db:        database,
		guard:     guard,
		extractor: extractor,
		batchSize: deepIndexBatchSize,
		pageSize:  deepIndexPageSize,
	}
}

// deepIndexBook is one unchecked book with every one of its files and its
// metadata-flow broken-read record from the work list (nil when it has none).
type deepIndexBook struct {
	id     int64
	files  []dbq.BookFile
	record *readRecord
}

// records returns a recordSource answering the read gate from the book's work
// row, so a deep index read queries no record.
func (b deepIndexBook) records() recordSource {
	return func(context.Context, int64, readFlow) (*readRecord, error) { return b.record, nil }
}

// deepIndexJob is one unit of worker input: the books whose top-ranked file
// shares an archive, or a single book otherwise. A worker reads a job in one
// session, so its archive is opened once.
type deepIndexJob struct {
	books []deepIndexBook
}

// deepIndexOutcome is a worker's verdict for one book: its metadata when the
// read is done, and the read's result.
type deepIndexOutcome struct {
	bookID int64
	meta   ebook.Metadata
	res    ReadResult
}

// Run indexes the library's unchecked books in scope, pageSize books at a
// time in id order, so only one page of the work list is held in memory. It
// reports the number of books in scope via rep.SetTotal and each committed
// batch via rep.Add. On cancellation it returns the counts of the batches
// committed so far together with ctx's error; those batches stay committed and
// the remaining books are picked up by the next run.
func (d *DeepIndexer) Run(
	ctx context.Context, library dbq.Library, scope DeepIndexScope, rep Reporter,
) (DeepIndexResult, error) {
	start := time.Now()

	res, err := d.runPages(ctx, library, scope, rep)

	d.log.Info("deep index finished",
		slog.Int64("library_id", library.ID),
		slog.Int("checked", res.Checked),
		slog.Int("filled", res.Filled),
		slog.Int("skipped", res.Skipped),
		slog.Int("broken", res.Broken),
		slog.Int("known_broken", res.KnownBroken),
		slog.Int("errors", res.Errors),
		slog.Duration("duration", time.Since(start)),
		slog.Bool("complete", err == nil),
	)

	return res, err
}

// groupByBook collects the work rows into books, in order of each book's first
// row (the list is ordered by source_path). Every row of a book carries the
// same record, so it is taken from the first.
func groupByBook(work []dbq.ListDeepIndexWorkRow) []deepIndexBook {
	var books []deepIndexBook
	byID := map[int64]int{}
	for _, row := range work {
		i, ok := byID[row.BookID]
		if !ok {
			i = len(books)
			byID[row.BookID] = i
			books = append(books, deepIndexBook{id: row.BookID, record: workRecord(row)})
		}
		books[i].files = append(books[i].files, dbq.BookFile{
			ID: row.FileID, BookID: row.BookID, FileFormat: row.FileFormat, SourcePath: row.SourcePath,
		})
	}

	return books
}

// workRecord returns the broken-read record joined onto a work row, or nil
// when the book has none. Only the columns the read gate compares are joined.
func workRecord(row dbq.ListDeepIndexWorkRow) *readRecord {
	if !row.FailureFingerprint.Valid || !row.FailureReaderVersion.Valid {
		return nil
	}

	return &readRecord{
		fingerprint:   row.FailureFingerprint.String,
		readerVersion: row.FailureReaderVersion.Int64,
	}
}

// runPages counts the work, then loads and processes it one page at a time,
// summing the pages' results. It stops at the first page that fails.
func (d *DeepIndexer) runPages(
	ctx context.Context, library dbq.Library, scope DeepIndexScope, rep Reporter,
) (DeepIndexResult, error) {
	q := dbq.New(d.db)
	total, err := q.CountDeepIndexWork(ctx, dbq.CountDeepIndexWorkParams{
		LibraryID: library.ID, ImportedAt: scope.importedAt,
	})
	if err != nil {
		return DeepIndexResult{}, fmt.Errorf("count deep index work: %w", err)
	}
	rep.SetTotal(int(total))

	var res DeepIndexResult
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return res, err //nolint:wrapcheck // expose the context cause to the caller
		}
		work, err := q.ListDeepIndexWork(ctx, dbq.ListDeepIndexWorkParams{
			LibraryID: library.ID, ImportedAt: scope.importedAt, AfterID: after, Lim: int64(d.pageSize),
		})
		if err != nil {
			return res, fmt.Errorf("list deep index work: %w", err)
		}
		if len(work) == 0 {
			return res, nil
		}
		books := groupByBook(work)
		for _, b := range books {
			after = max(after, b.id)
		}
		page, err := d.process(ctx, library, d.jobs(library, books), rep)
		res.add(page)
		if err != nil {
			return res, err
		}
	}
}

// jobs groups books by resolver.group of their top-ranked describing file: one
// job per archive, and one job per book whose file is not archive-backed or
// that has no file to read.
func (d *DeepIndexer) jobs(library dbq.Library, books []deepIndexBook) []deepIndexJob {
	var jobs []deepIndexJob
	byGroup := map[string]int{}
	for _, book := range books {
		key := ""
		if top := d.extractor.candidates(flowMetadata, book.files); len(top) > 0 {
			key = d.extractor.res.group(library, top[0])
		}
		if key == "" {
			jobs = append(jobs, deepIndexJob{books: []deepIndexBook{book}})
			continue
		}
		i, ok := byGroup[key]
		if !ok {
			i = len(jobs)
			byGroup[key] = i
			jobs = append(jobs, deepIndexJob{})
		}
		jobs[i].books = append(jobs[i].books, book)
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

// runJob produces one outcome per book of the job, reading them all in one
// session. Every send selects on ctx so a worker never blocks once the writer
// has stopped.
func (d *DeepIndexer) runJob(
	ctx context.Context, library dbq.Library, job deepIndexJob, out chan<- deepIndexOutcome,
) {
	sess := d.extractor.newSession()
	defer sess.close()

	for _, book := range job.books {
		if ctx.Err() != nil {
			return
		}
		select {
		case out <- d.read(ctx, sess, library, book):
		case <-ctx.Done():
			return
		}
	}
}

// read describes one book from the best of its files, gated by the record from
// its work row. A book with no file read for descriptions is done with empty
// metadata and is marked checked unread.
func (d *DeepIndexer) read(
	ctx context.Context, sess *readSession, library dbq.Library, book deepIndexBook,
) deepIndexOutcome {
	meta, res := sess.description(ctx, library, book.files, book.records())

	return deepIndexOutcome{bookID: book.id, meta: meta, res: res}
}

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
		res.add(tally)
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
// returns its tally, which only counts once the transaction has committed. A
// done book is persisted, its read failure record deleted and it is marked
// checked; a newly broken book is recorded (and logged once the batch has
// committed); a known broken or retryable book is not written.
func (d *DeepIndexer) commit(ctx context.Context, batch []deepIndexOutcome) (DeepIndexResult, error) {
	var (
		tally  DeepIndexResult
		broken []*deepIndexOutcome
	)
	err := d.guard.WithTx(ctx, d.db, func(tx *sql.Tx) error {
		tally, broken = DeepIndexResult{}, broken[:0]
		q := dbq.New(tx)
		for i := range batch {
			newlyBroken, err := d.apply(ctx, tx, q, &batch[i], &tally)
			if err != nil {
				return fmt.Errorf("book %d: %w", batch[i].bookID, err)
			}
			if newlyBroken {
				broken = append(broken, &batch[i])
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
	for _, o := range broken {
		d.log.Warn("unreadable book file",
			slog.Int64("book", o.bookID),
			slog.String("flow", string(flowMetadata)),
			slog.String("reason", string(o.res.Reason)),
			slog.String("detail", o.res.Detail),
		)
	}

	return tally, nil
}

// apply writes one outcome inside the batch transaction and counts it in
// tally. It reports whether the book was newly recorded as broken. A book
// deleted since the work list was read is neither written nor counted.
func (d *DeepIndexer) apply(
	ctx context.Context, tx *sql.Tx, q *dbq.Queries, o *deepIndexOutcome, tally *DeepIndexResult,
) (newlyBroken bool, err error) {
	switch {
	case o.res.Status == ReadRetryable:
		d.log.Debug("deep index: book left unchecked", slog.Int64("book", o.bookID), slog.Any("error", o.res.Err))
		tally.Errors++
	case o.res.Status == ReadBroken && o.res.Known:
		tally.KnownBroken++
	case o.res.Status == ReadBroken:
		recorded, err := d.recordBroken(ctx, q, o)
		if err != nil {
			return false, err
		}
		if recorded {
			tally.Broken++
		}

		return recorded, nil
	default:
		return false, d.applyDone(ctx, tx, q, o, tally)
	}

	return false, nil
}

// applyDone persists a done book and counts it in tally.
func (d *DeepIndexer) applyDone(
	ctx context.Context, tx *sql.Tx, q *dbq.Queries, o *deepIndexOutcome, tally *DeepIndexResult,
) error {
	filled, skipped, err := d.persist(ctx, tx, q, o)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tally.Checked++
	if filled {
		tally.Filled++
	}
	if skipped {
		tally.Skipped++
	}

	return nil
}

// recordBroken stores a newly broken book's read failure record. It reports
// false, writing nothing, when the book was deleted since the work list was
// read.
func (d *DeepIndexer) recordBroken(ctx context.Context, q *dbq.Queries, o *deepIndexOutcome) (bool, error) {
	if _, err := q.BookHasAnnotation(ctx, o.bookID); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("probe book: %w", err)
	}
	if err := q.UpsertReadFailure(ctx, readFailureParams(o.bookID, flowMetadata, o.res)); err != nil {
		return false, fmt.Errorf("record read failure: %w", err)
	}

	return true, nil
}

// persist stores one done book's outcome: the annotation only when the book has
// none (plus its FTS row), identifiers insert-if-absent, then it deletes the
// book's metadata read failure record and sets the checked mark.
func (d *DeepIndexer) persist(
	ctx context.Context, tx *sql.Tx, q *dbq.Queries, o *deepIndexOutcome,
) (filled, skipped bool, err error) {
	filled, skipped, err = d.fillAnnotation(ctx, tx, q, o)
	if err != nil {
		return false, false, err
	}
	for _, id := range o.meta.Identifiers {
		idErr := q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
			BookID: o.bookID, Type: id.Type, Value: id.Value,
		})
		if idErr != nil {
			return false, false, fmt.Errorf("insert identifier: %w", idErr)
		}
	}
	err = q.DeleteReadFailure(ctx, dbq.DeleteReadFailureParams{BookID: o.bookID, Flow: string(flowMetadata)})
	if err != nil {
		return false, false, fmt.Errorf("delete read failure: %w", err)
	}
	if err = q.MarkMetadataChecked(ctx, o.bookID); err != nil {
		return false, false, fmt.Errorf("mark checked: %w", err)
	}

	return filled, skipped, nil
}

// fillAnnotation writes the recovered annotation (and refreshes the FTS row)
// only when the book has none. It reports whether the annotation was filled,
// or skipped because the book already had one.
func (d *DeepIndexer) fillAnnotation(
	ctx context.Context, tx *sql.Tx, q *dbq.Queries, o *deepIndexOutcome,
) (filled, skipped bool, err error) {
	has, err := q.BookHasAnnotation(ctx, o.bookID)
	if err != nil {
		return false, false, fmt.Errorf("probe annotation: %w", err)
	}
	if has {
		return false, true, nil
	}
	if strings.TrimSpace(o.meta.Annotation) == "" {
		return false, false, nil
	}
	n, err := q.SetBookAnnotationIfNull(ctx, dbq.SetBookAnnotationIfNullParams{
		Annotation: sql.NullString{String: o.meta.Annotation, Valid: true}, ID: o.bookID,
	})
	if err != nil {
		return false, false, fmt.Errorf("set annotation: %w", err)
	}
	if n == 0 {
		return false, false, nil
	}
	if err = db.RefreshBookFTS(ctx, tx, o.bookID); err != nil {
		return false, false, fmt.Errorf("refresh fts: %w", err)
	}

	return true, false, nil
}
