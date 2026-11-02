package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// backfillAcquireBudget bounds how long a backfill waits for the single-writer
// guard before giving up and leaving the book unchecked for a later retry. It
// mirrors the API write budget so a backfill can never queue behind a long
// indexing run.
const backfillAcquireBudget = 2 * time.Second

// FileExtractor recovers a book's metadata from its own source file. *Extractor
// satisfies it. Declared here, beside its only consumer (LocalBackfiller).
type FileExtractor interface {
	// Description returns the metadata read from the book's source file
	// (annotation, identifiers, ...), identifiers already cleaned, and the
	// outcome of the read. A done read of a book with nothing readable (missing
	// book, or only files that are not read for descriptions, such as PDF) has
	// empty metadata.
	Description(ctx context.Context, bookID int64) (ebook.Metadata, ReadResult)
}

// LocalBackfiller recovers a book's offline metadata (annotation + identifiers)
// from its own file and persists the gaps, at most once per book. It is the
// shared tier behind the REST first-view path, so an INPX book's FB2 annotation
// lands in the DB for every reader (REST and OPDS), not only after a web-UI view.
// Best-effort throughout.
type LocalBackfiller struct {
	log       *slog.Logger
	db        *sql.DB
	q         *dbq.Queries
	guard     *db.WriteGuard
	extractor FileExtractor
	sf        singleflight.Group
}

// NewLocalBackfiller builds the offline backfill tier.
func NewLocalBackfiller(
	log *slog.Logger, database *sql.DB, guard *db.WriteGuard, extractor FileExtractor,
) *LocalBackfiller {
	return &LocalBackfiller{
		log:       log,
		db:        database,
		q:         dbq.New(database),
		guard:     guard,
		extractor: extractor,
	}
}

// Fill recovers and persists one book's offline metadata, at most once
// (metadata_checked gated). Concurrent calls for the same book id collapse via
// single-flight so the same file is never parsed twice concurrently.
func (b *LocalBackfiller) Fill(ctx context.Context, bookID int64) error {
	// ctx is captured from the caller; safe because fill always returns nil (never propagates ctx errors).
	_, err, _ := b.sf.Do(strconv.FormatInt(bookID, 10), func() (any, error) {
		return nil, b.fill(ctx, bookID)
	})

	return err //nolint:wrapcheck // singleflight re-surfaces the error from fill; wrapping adds no value
}

func (b *LocalBackfiller) fill(ctx context.Context, bookID int64) error {
	book, err := b.q.GetBook(ctx, bookID)
	if err != nil {
		return nil // book gone or unreadable; nothing to do
	}
	if book.MetadataChecked != 0 {
		return nil // already backfilled
	}

	meta, res := b.extractor.Description(ctx, bookID)
	switch res.Status {
	case ReadDone:
		// fall through to persist below
	case ReadBroken:
		b.recordBroken(ctx, bookID, res)
		return nil
	case ReadRetryable: // nothing is recorded, a later call retries (FR-016)
		b.log.Debug("backfill: read failed, will retry", slog.Int64("book", bookID), slog.Any("error", res.Err))
		return nil
	}

	// File I/O above ran outside the guard; take the single-writer guard only
	// around the DB writes. Best-effort: if an indexing run holds it past the
	// budget, skip rather than stall — the book stays unchecked and a later call
	// retries. The writes are detached from the caller's cancellation: the file
	// is already read, so an aborted request must not throw that work away.
	ctx = context.WithoutCancel(ctx)
	gctx, cancel := context.WithTimeout(ctx, backfillAcquireBudget)
	defer cancel()
	if lockErr := b.guard.Lock(gctx); lockErr != nil {
		return nil
	}
	defer b.guard.Unlock()

	// The record goes first: persist marks the book checked, which would
	// otherwise seal it with a stale failure record still in place.
	err = b.q.DeleteReadFailure(ctx, dbq.DeleteReadFailureParams{BookID: bookID, Flow: string(flowMetadata)})
	if err != nil {
		// Leave the book pending: sealing it with metadata_checked would
		// strand the stale record, so the next view tries again.
		b.log.Warn("backfill: clear read failure", slog.Int64("book", bookID), slog.Any("error", err))

		return nil
	}

	b.persist(ctx, book, meta)

	return nil
}

// persist stores what the file read recovered and marks the book checked. The
// caller holds the write guard.
func (b *LocalBackfiller) persist(ctx context.Context, book dbq.Book, meta ebook.Metadata) {
	if !book.Annotation.Valid {
		if err := b.persistAnnotation(ctx, book.ID, meta.Annotation); err != nil {
			// The write rolled back: leave the book unchecked so the next view
			// retries instead of sealing it without its description.
			b.log.Warn("backfill: persist annotation", slog.Int64("book", book.ID), slog.Any("error", err))

			return
		}
	}
	b.persistIdentifiers(ctx, book.ID, meta.Identifiers)
	if err := b.q.MarkMetadataChecked(ctx, book.ID); err != nil {
		b.log.Warn("backfill: mark checked", slog.Int64("book", book.ID), slog.Any("error", err))
	}
}

// recordBroken remembers an unreadable book so later views do not re-read it,
// and logs it once. A result already known from a stored record is neither
// written nor logged. The book stays unchecked. Best-effort: a busy guard skips
// the write (and the log), and the next view tries again.
func (b *LocalBackfiller) recordBroken(ctx context.Context, bookID int64, res ReadResult) {
	if res.Known {
		return
	}
	recordReadFailure(ctx, b.log, b.guard, b.q, bookID, flowMetadata, res)
}

// persistAnnotation stores a recovered annotation on the book row and FTS index.
// A blank annotation is a no-op.
func (b *LocalBackfiller) persistAnnotation(ctx context.Context, bookID int64, annotation string) error {
	if strings.TrimSpace(annotation) == "" {
		return nil
	}

	return b.writeAnnotation(ctx, bookID, annotation)
}

// writeAnnotation stores the annotation and refreshes the FTS row in one
// transaction, so a failed refresh rolls the annotation back and the book is
// retried instead of being left stored but unindexed. The write only lands on
// a book still without an annotation: the file was read outside the guard, so
// an enrichment or manual edit may have stored one meanwhile, and that wins.
// The caller holds the write guard.
func (b *LocalBackfiller) writeAnnotation(ctx context.Context, bookID int64, annotation string) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	n, err := b.q.WithTx(tx).SetBookAnnotationIfNull(ctx, dbq.SetBookAnnotationIfNullParams{
		Annotation: sql.NullString{String: annotation, Valid: true}, ID: bookID,
	})
	if err != nil {
		return fmt.Errorf("update annotation: %w", err)
	}
	if n == 0 {
		return nil // an annotation arrived meanwhile; nothing changed to index
	}
	if err := db.RefreshBookFTS(ctx, tx, bookID); err != nil {
		return fmt.Errorf("refresh fts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// persistIdentifiers gap-fills cleaned identifiers (insert-if-absent), so a
// file-recovered id never downgrades a sync-stored one. Best-effort.
func (b *LocalBackfiller) persistIdentifiers(ctx context.Context, bookID int64, ids []ebook.Identifier) {
	for _, id := range ids {
		err := b.q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
			BookID: bookID, Type: id.Type, Value: id.Value,
		})
		if err != nil {
			b.log.Warn("backfill: persist identifier", slog.Int64("book", bookID), slog.Any("error", err))
		}
	}
}
