package ingest

import (
	"context"
	"log/slog"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// recordAcquireBudget bounds how long a best-effort record write waits for the
// write guard before skipping it, so a read on a request path never queues
// behind a long indexing run.
const recordAcquireBudget = backfillAcquireBudget

// readFailureParams returns the book_read_failures row recording a broken read
// of (bookID, flow) under the current reader version.
func readFailureParams(bookID int64, flow readFlow, res ReadResult) dbq.UpsertReadFailureParams {
	return dbq.UpsertReadFailureParams{
		BookID:        bookID,
		Flow:          string(flow),
		Reason:        string(res.Reason),
		Detail:        res.Detail,
		Fingerprint:   res.Fingerprint,
		ReaderVersion: ebook.ReaderVersion,
		RecordedAt:    time.Now().Unix(),
	}
}

// recordReadFailure stores a newly broken read of (bookID, flow) under the
// write guard and logs the book once. Best-effort: a guard still busy after
// recordAcquireBudget skips the write and the log, and a failed write is
// logged instead, so a later read tries again. The write is detached from
// ctx's cancellation: the file was already read, and a request aborted at this
// point (a closed tab, a cancelled thumbnail) must not cost a re-read next time.
func recordReadFailure(
	ctx context.Context, log *slog.Logger, guard *db.WriteGuard, q *dbq.Queries,
	bookID int64, flow readFlow, res ReadResult,
) {
	ctx = context.WithoutCancel(ctx)
	gctx, cancel := context.WithTimeout(ctx, recordAcquireBudget)
	defer cancel()
	if err := guard.Lock(gctx); err != nil {
		return
	}
	defer guard.Unlock()

	if err := q.UpsertReadFailure(ctx, readFailureParams(bookID, flow, res)); err != nil {
		log.Warn("record read failure", slog.Int64("book", bookID), slog.String("flow", string(flow)),
			slog.Any("error", err))
		return
	}
	log.Warn("unreadable book file", slog.Int64("book", bookID), slog.String("flow", string(flow)),
		slog.String("reason", string(res.Reason)), slog.String("detail", res.Detail))
}

// clearReadFailure deletes the (bookID, flow) broken-read record, if any,
// under the write guard. Best-effort like recordReadFailure: a busy guard skips
// the delete, and a failed delete is logged. Detached from ctx's cancellation
// for the same reason.
func clearReadFailure(
	ctx context.Context, log *slog.Logger, guard *db.WriteGuard, q *dbq.Queries, bookID int64, flow readFlow,
) {
	ctx = context.WithoutCancel(ctx)
	gctx, cancel := context.WithTimeout(ctx, recordAcquireBudget)
	defer cancel()
	if err := guard.Lock(gctx); err != nil {
		return
	}
	defer guard.Unlock()

	err := q.DeleteReadFailure(ctx, dbq.DeleteReadFailureParams{BookID: bookID, Flow: string(flow)})
	if err != nil {
		log.Warn("clear read failure", slog.Int64("book", bookID), slog.String("flow", string(flow)),
			slog.Any("error", err))
	}
}
