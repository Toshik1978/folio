package ingest

import (
	"context"
	"fmt"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// reconciler diffs a library's freshly-read set of records against what is
// already stored, upserting books by (library_id, library_key) and their files by
// source_path and pruning anything not seen. It replaces wipe-and-reinsert so
// book ids (and cached covers) survive re-syncs.
type reconciler struct {
	im        *importer
	libraryID int64
	now       int64
	prev      map[string]dbq.ListBookFilesByLibraryRow // by source_path
	seen      map[string]struct{}
	added     int
	r         Reporter
}

// newReconciler snapshots the library's current files for diffing.
func newReconciler(ctx context.Context, im *importer, libraryID, now int64, r Reporter) (*reconciler, error) {
	known, err := dbq.New(im.db).ListBookFilesByLibrary(ctx, libraryID)
	if err != nil {
		return nil, fmt.Errorf("list library files: %w", err)
	}
	prev := make(map[string]dbq.ListBookFilesByLibraryRow, len(known))
	for i := range known {
		prev[known[i].SourcePath] = known[i]
	}

	return &reconciler{
		im: im, libraryID: libraryID, now: now,
		prev: prev, seen: make(map[string]struct{}),
		r: r,
	}, nil
}

// upsert persists one record and marks its file seen. A file whose source_path
// previously belonged to a different logical book (its grouping key changed) has
// its stale row removed so it does not linger under the old book.
func (rc *reconciler) upsert(ctx context.Context, rec bookRecord) error {
	prev, existed := rc.prev[rec.SourcePath]
	if !existed {
		rc.added++ // count only genuinely new files; a changed file is not an add
	}
	rc.seen[rec.SourcePath] = struct{}{}

	bookID, err := rc.im.add(ctx, rec, rc.now)
	if err != nil {
		return err
	}
	rc.r.Add(1) // count after the record is persisted, not merely attempted
	if existed && prev.BookID != bookID {
		q, err := rc.im.getQueries(ctx)
		if err != nil {
			return err
		}
		if err := q.DeleteBookFile(ctx, prev.ID); err != nil {
			return fmt.Errorf("drop moved file %d: %w", prev.ID, err)
		}
	}

	return nil
}

// markSeen protects an unchanged file from pruning without re-parsing it.
func (rc *reconciler) markSeen(sourcePath string) {
	rc.seen[sourcePath] = struct{}{}
	rc.r.Add(1)
}

// prune deletes files no longer present in the library, then removes any book
// left with no files (covering both pruned files and books emptied by a file
// moving to a new grouping key).
func (rc *reconciler) prune(ctx context.Context) (int, error) {
	q, err := rc.im.getQueries(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for path, f := range rc.prev {
		if _, ok := rc.seen[path]; ok {
			continue
		}
		if delErr := q.DeleteBookFile(ctx, f.ID); delErr != nil {
			return removed, fmt.Errorf("delete file %d: %w", f.ID, delErr)
		}
		removed++
	}

	ids, err := q.ListEmptyBooksByLibrary(ctx, rc.libraryID)
	if err != nil {
		return removed, fmt.Errorf("list empty books: %w", err)
	}
	for _, bookID := range ids {
		if err := rc.im.remove(ctx, bookID); err != nil {
			return removed, err
		}
	}

	return removed, nil
}
