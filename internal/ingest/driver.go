package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// scanFunc runs one source's iteration against rc. The driver owns the
// importer/reconciler lifecycle; the source owns how it enumerates records.
type scanFunc func(ctx context.Context, rc *reconciler) error

// runReconcile owns the import lifecycle every source shares: open the batched
// importer, build the reconciler, run the source's scan, prune vanished files,
// and commit (rolling back on any error). Sources keep their own per-source
// diffing strategy (folder's size+mtime skip, Calibre/INPX content-hash merge)
// inside scan. reader's Rank is the format preference handed to the importer.
//
// After the commit, still inside the sync run and so under the engine's write
// guard, the library's broken-read records are re-checked against their files'
// fingerprints (recheckReadFailures). The re-check is best-effort: a failure is
// logged and the sync result stands.
func runReconcile(
	ctx context.Context,
	db *sql.DB,
	covers CoverStore,
	library dbq.Library,
	r Reporter,
	log *slog.Logger,
	reader bookReader,
	scan scanFunc,
) (Result, error) {
	im := newImporter(log, db, covers, 1000, reader.Rank)
	defer im.rollback()

	now := time.Now().Unix()
	rc, err := newReconciler(ctx, im, library.ID, now, r)
	if err != nil {
		return Result{}, err
	}
	if err2 := scan(ctx, rc); err2 != nil {
		return Result{}, err2
	}

	removed, removedBooks, err := rc.prune(ctx)
	if err != nil {
		return Result{}, err
	}
	if err = im.commit(); err != nil {
		return Result{}, fmt.Errorf("commit sync: %w", err)
	}
	lifted, err := recheckReadFailures(ctx, log, db, reader, library)
	switch {
	case err != nil && !errors.Is(err, context.Canceled):
		log.Warn("read failure re-check failed; the sync result stands",
			slog.Int64("library", library.ID), slog.Any("error", err))
	case lifted > 0:
		log.Info("lifted stale read failure records",
			slog.Int64("library", library.ID), slog.Int("lifted", lifted))
	}

	return Result{Added: rc.added, Removed: removed, RemovedBooks: removedBooks, ImportedAt: now}, nil
}
