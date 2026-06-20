package sync

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// checkPurge deletes every library whose purge deadline has elapsed and prunes
// its scheduled jobs afterward.
func (e *Engine) checkPurge(ctx context.Context) {
	now := e.now().Unix()
	expired, err := dbq.New(e.db).ListPendingPurgeLibraries(ctx, sql.NullInt64{Int64: now, Valid: true})
	if err != nil {
		e.log.Error("list pending purge libraries", slog.Any("error", err))
		return
	}
	if len(expired) == 0 {
		return
	}

	for i := range expired {
		if err := e.purgeLibrary(ctx, expired[i].ID); err != nil {
			e.log.Error("purge library", slog.Int64("library", expired[i].ID), slog.Any("error", err))
			continue
		}
		e.log.Info("purged library", slog.Int64("library", expired[i].ID), slog.String("path", expired[i].Path))
	}

	if err := e.Reschedule(ctx); err != nil {
		e.log.Error("reschedule after purge", slog.Any("error", err))
	}
}

// RequestPurge runs PurgeLibrary on a background goroutine — the asynchronous
// form behind POST /api/libraries/{id}/purge, which answers 202 immediately
// instead of blocking (potentially for a full sync's duration) on writeMu, and
// runs the multi-step teardown on context.Background() so a client disconnect
// can't cancel it between steps (covers deleted but rows kept, etc.). Failures
// are logged; the row simply stays pending_purge for the deadline sweep to
// retry. Stop waits for in-flight purges via e.bg.
func (e *Engine) RequestPurge(id int64) {
	e.bg.Go(func() {
		if err := e.PurgeLibrary(context.Background(), id); err != nil {
			e.log.Error("purge library", slog.Int64("library", id), slog.Any("error", err))
		}
	})
}

// PurgeLibrary reclaims a library's data immediately — the on-demand counterpart
// to the deadline-driven checkPurge — then reschedules the job set. It is safe
// to call for any library; the API exposes it as "Purge Now" to skip the grace
// period.
func (e *Engine) PurgeLibrary(ctx context.Context, id int64) error {
	if err := e.purgeLibrary(ctx, id); err != nil {
		return err
	}
	if err := e.Reschedule(ctx); err != nil {
		return fmt.Errorf("reschedule after purge: %w", err)
	}

	return nil
}

// purgeLibrary cascade-deletes a library: it evicts each book's cached cover
// (best-effort — a stuck cover file must not block reclaiming the rows), then
// deletes the books and the library row in one transaction so a crash mid-teardown
// can never leave an empty-but-present library. Deleting the books explicitly
// (rather than relying on the library FK cascade) keeps the books_ad FTS-cleanup
// trigger firing per row.
func (e *Engine) purgeLibrary(ctx context.Context, id int64) error {
	// Serialize against an in-flight sync (the shared single-writer lock) so the
	// cascade delete never races an indexing transaction; see Engine.writeMu.
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	q := dbq.New(e.db)

	ids, err := q.ListBookIDsByLibrary(ctx, id)
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	for _, bookID := range ids {
		if delErr := e.covers.Delete(bookID); delErr != nil {
			e.log.Warn("purge: delete cover", slog.Int64("book", bookID), slog.Any("error", delErr))
		}
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	tq := dbq.New(tx)

	if err := tq.DeleteBooksByLibrary(ctx, id); err != nil {
		return fmt.Errorf("delete books: %w", err)
	}
	if err := tq.DeleteLibrary(ctx, id); err != nil {
		return fmt.Errorf("delete library: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge: %w", err)
	}

	// Notify before emitting so SSE clients that immediately refetch stats
	// see a fresh value (mirrors the ordering in recordLastSync).
	if e.statsObserver != nil {
		e.statsObserver.StatsChanged()
	}

	// Notify SSE clients the row is gone so they refetch and drop it, mirroring
	// the post-sync emitLibrary. Without this a "Purge Now" (whose async teardown
	// finishes after the handler's 202) leaves the deleted library stuck on
	// "Pending Purge" until a manual reload.
	e.emitLibrary(id, statusPurged)

	return nil
}
