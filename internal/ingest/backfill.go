package ingest

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// backfillAcquireBudget bounds how long a backfill waits for the single-writer
// guard before giving up and leaving the book unchecked for a later retry. It
// mirrors the API write budget so a backfill can never queue behind a long
// indexing run.
const backfillAcquireBudget = 2 * time.Second

// FileExtractor recovers a book's metadata from its own source file. *Extractor
// satisfies it. Declared here, beside its only consumer (LocalBackfiller).
type FileExtractor interface {
	// Backfill returns metadata parsed from the book's source file, identifiers
	// already cleaned. ok is false when nothing parseable was found (missing book,
	// skipped/unsupported format such as PDF).
	Backfill(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
}

// LocalBackfiller recovers a book's offline metadata (annotation + identifiers)
// from its own file and persists the gaps, at most once per book. It is the
// shared tier behind both the REST first-view path and the sync warmer, so an
// INPX book's FB2 annotation lands in the DB for every reader (REST and OPDS),
// not only after a web-UI view. Best-effort throughout.
type LocalBackfiller struct {
	log       *slog.Logger
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
		q:         dbq.New(database),
		guard:     guard,
		extractor: extractor,
	}
}

// Fill recovers and persists one book's offline metadata, at most once
// (metadata_checked gated). Concurrent calls for the same book id collapse via
// single-flight, so REST and the warmer never double-parse the same file.
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

	meta, ok, err := b.extractor.Backfill(ctx, bookID)
	if err != nil {
		b.log.Warn("backfill: extract", slog.Int64("book", bookID), slog.Any("error", err))
		return nil // transient → leave unchecked, retry later
	}

	// File I/O above ran outside the guard; take the single-writer guard only
	// around the DB writes. Best-effort: if an indexing run holds it past the
	// budget, skip rather than stall — the book stays unchecked and a later call
	// retries.
	gctx, cancel := context.WithTimeout(ctx, backfillAcquireBudget)
	defer cancel()
	if err := b.guard.Lock(gctx); err != nil {
		return nil
	}
	defer b.guard.Unlock()

	if ok {
		if !book.Annotation.Valid {
			b.persistAnnotation(ctx, bookID, meta.Annotation)
		}
		b.persistIdentifiers(ctx, bookID, meta.Identifiers)
	}
	if err := b.q.MarkMetadataChecked(ctx, bookID); err != nil {
		b.log.Warn("backfill: mark checked", slog.Int64("book", bookID), slog.Any("error", err))
	}

	return nil
}

// persistAnnotation stores a recovered annotation on the book row and FTS index.
// A blank annotation is a no-op.
func (b *LocalBackfiller) persistAnnotation(ctx context.Context, bookID int64, annotation string) {
	if strings.TrimSpace(annotation) == "" {
		return
	}
	if err := b.q.UpdateBookAnnotation(ctx, dbq.UpdateBookAnnotationParams{
		Annotation: sql.NullString{String: annotation, Valid: true}, ID: bookID,
	}); err != nil {
		b.log.Warn("backfill: persist annotation", slog.Int64("book", bookID), slog.Any("error", err))
		return
	}
	if err := b.q.UpdateBookFTSAnnotation(ctx, dbq.UpdateBookFTSAnnotationParams{
		Annotation: htmltext.StripMarkup(annotation), BookID: strconv.FormatInt(bookID, 10),
	}); err != nil {
		b.log.Warn("backfill: persist annotation fts", slog.Int64("book", bookID), slog.Any("error", err))
	}
}

// persistIdentifiers gap-fills cleaned identifiers (insert-if-absent), so a
// file-recovered id never downgrades a sync-stored one. Best-effort.
func (b *LocalBackfiller) persistIdentifiers(ctx context.Context, bookID int64, ids []ebook.Identifier) {
	for _, id := range ids {
		if err := b.q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
			BookID: bookID, Type: id.Type, Value: id.Value,
		}); err != nil {
			b.log.Warn("backfill: persist identifier", slog.Int64("book", bookID), slog.Any("error", err))
		}
	}
}
