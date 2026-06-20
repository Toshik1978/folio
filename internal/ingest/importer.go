package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// undefinedLanguage represents an undefined or unspecified language.
const undefinedLanguage = "und"

// importer wraps the writable folio database and cover store, providing a
// single place that knows how to persist a bookRecord (book row + authors +
// genres + series + FTS entry) atomically and cache its cover.
type importer struct {
	db        *sql.DB
	tx        *sql.Tx
	queries   *dbq.Queries
	covers    CoverStore
	coverPrio map[int64]int // bookID -> priority of the cover currently saved this run
	// newBooks are books inserted in the current uncommitted batch. Their covers
	// are written to disk eagerly, but AUTOINCREMENT ids are never reused, so a
	// rolled-back batch would orphan those files forever; rollback deletes them.
	newBooks  []int64
	count     int
	batchSize int
}

// newImporter builds an importer with an initialized cover-priority tracker.
func newImporter(db *sql.DB, covers CoverStore) *importer {
	return &importer{
		db:        db,
		covers:    covers,
		coverPrio: map[int64]int{},
		batchSize: 1,
	}
}

func (im *importer) setBatchSize(size int) {
	if size > 0 {
		im.batchSize = size
	}
}

func (im *importer) begin(ctx context.Context) error {
	if im.tx != nil {
		return nil
	}
	tx, err := im.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import tx: %w", err)
	}
	im.tx = tx
	im.queries = dbq.New(tx)

	return nil
}

func (im *importer) commit() error {
	if im.tx == nil {
		return nil
	}
	err := im.tx.Commit()
	im.tx = nil
	im.queries = nil
	if err != nil {
		// newBooks intentionally survives a failed commit: the rows never
		// persisted, so the deferred rollback must still delete their covers.
		return fmt.Errorf("commit import tx: %w", err)
	}
	im.newBooks = nil

	return nil
}

func (im *importer) rollback() {
	if im.tx != nil {
		_ = im.tx.Rollback()
		im.tx = nil
		im.queries = nil
	}
	for _, id := range im.newBooks {
		_ = im.covers.Delete(id) // best-effort orphan cleanup
	}
	im.newBooks = nil
}

func (im *importer) getQueries(ctx context.Context) (*dbq.Queries, error) {
	if err := im.begin(ctx); err != nil {
		return nil, err
	}
	return im.queries, nil
}

// add persists rec, grouping it onto the existing logical book for its
// (library_id, library_key) when one exists (ensuring its format file and
// gap-filling missing metadata) or creating a new book otherwise. It returns the
// logical book id and, on success, caches the cover if this file's format
// outranks any already saved.
func (im *importer) add(ctx context.Context, rec bookRecord, addedAt int64) (int64, error) {
	q, err := im.getQueries(ctx)
	if err != nil {
		return 0, err
	}

	existing, err := q.FindBookByLibraryKey(ctx, dbq.FindBookByLibraryKeyParams{
		LibraryID: rec.LibraryID, LibraryKey: rec.LibraryKey,
	})
	var (
		bookID    int64
		coverPrio int64
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		bookID, err = insertBook(ctx, q, rec, resolveAddedAt(rec, addedAt), addedAt)
		if err != nil {
			return 0, err
		}
		im.newBooks = append(im.newBooks, bookID)
	case err != nil:
		return 0, fmt.Errorf("find book by key: %w", err)
	default:
		bookID = existing.ID
		coverPrio = existing.CoverPrio
		if err := mergeExisting(ctx, q, existing, rec); err != nil {
			return 0, err
		}
	}

	im.saveCoverIfBetter(ctx, bookID, coverPrio, rec)

	im.count++
	if im.count >= im.batchSize {
		if err := im.commit(); err != nil {
			return 0, fmt.Errorf("commit batch: %w", err)
		}
		im.count = 0
	}

	return bookID, nil
}

// saveCoverIfBetter caches rec's cover under bookID when this file's format
// outranks both any cover already saved this run and persistedPrio — the
// priority of the cover currently on disk (books.cover_prio), which survives
// runs, so a partial re-sync of a low-priority edition can never downgrade a
// richer edition's cover. Equal priority still saves: the owning format's cover
// bytes may have changed, and covers.Save skips identical content. The persisted
// priority is restamped only when it actually rises. Best-effort, like the save.
func (im *importer) saveCoverIfBetter(ctx context.Context, bookID, persistedPrio int64, rec bookRecord) {
	if len(rec.Cover) == 0 {
		return
	}
	p := int64(filePriority(rec.FileFormat))
	if p < persistedPrio {
		return
	}
	if cur, ok := im.coverPrio[bookID]; ok && int(p) <= cur {
		return
	}
	if err := im.covers.Save(bookID, rec.Cover); err != nil {
		return
	}
	im.coverPrio[bookID] = int(p)
	if p == persistedPrio {
		return
	}
	if q, err := im.getQueries(ctx); err == nil {
		_ = q.UpdateBookCoverPrio(ctx, dbq.UpdateBookCoverPrioParams{CoverPrio: p, ID: bookID})
	}
}

// remove deletes a book by id (cascading authors/genres/FTS) and evicts its
// cached cover.
func (im *importer) remove(ctx context.Context, bookID int64) error {
	q, err := im.getQueries(ctx)
	if err != nil {
		return err
	}
	if err := q.DeleteBook(ctx, bookID); err != nil {
		return fmt.Errorf("delete book %d: %w", bookID, err)
	}
	if err := im.covers.Delete(bookID); err != nil {
		return fmt.Errorf("delete cover %d: %w", bookID, err)
	}

	return nil
}
