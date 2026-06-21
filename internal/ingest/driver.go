package ingest

import (
	"context"
	"database/sql"
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
// inside scan.
func runReconcile(
	ctx context.Context,
	db *sql.DB,
	covers CoverStore,
	library dbq.Library,
	r Reporter,
	log *slog.Logger,
	scan scanFunc,
) (Result, error) {
	im := newImporter(db, covers)
	im.setBatchSize(1000)
	im.setLogger(log)
	defer im.rollback()

	rc, err := newReconciler(ctx, im, library.ID, time.Now().Unix(), r)
	if err != nil {
		return Result{}, err
	}
	if err2 := scan(ctx, rc); err2 != nil {
		return Result{}, err2
	}

	removed, err := rc.prune(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := im.commit(); err != nil {
		return Result{}, fmt.Errorf("commit sync: %w", err)
	}

	return Result{Added: rc.added, Removed: removed}, nil
}
