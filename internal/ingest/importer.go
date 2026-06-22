package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// undefinedLanguage represents an undefined or unspecified language.
const undefinedLanguage = "und"

// msgIdentifierOverride is logged when identifier grouping links a record onto a
// book whose library_key differs from the record's own — i.e. strong-identifier
// matching overrode key-based grouping. This is the visibility hook for spotting
// a wrong auto-merge (reused/garbage ISBN), though it also fires on the intended
// author-order-drift heals; the attributes let a human tell them apart.
const msgIdentifierOverride = "identifier grouping overrode key-based grouping"

// coverOp is a deferred cover filesystem mutation: non-nil data means Save,
// nil data means Delete. Ops are queued during a batch and replayed on commit.
type coverOp struct {
	bookID int64
	data   []byte // non-nil = Save; nil = Delete
}

// importer wraps the writable folio database and cover store, providing a
// single place that knows how to persist a bookRecord (book row + authors +
// genres + series + FTS entry) atomically and cache its cover.
type importer struct {
	log       *slog.Logger
	db        *sql.DB
	tx        *sql.Tx
	queries   *dbq.Queries
	covers    CoverStore
	batchSize int
	coverPrio map[int64]int // bookID -> priority of the cover currently saved this run
	// pendingCovers holds cover filesystem mutations deferred until commit, so a
	// rolled-back batch never leaves a cover that disagrees with the DB.
	pendingCovers []coverOp
	count         int
}

// newImporter builds an importer with an initialized cover-priority tracker. The
// logger defaults to a discard handler so callers (and tests) that don't wire one
// stay silent; production wires the real logger via setLogger.
func newImporter(log *slog.Logger, db *sql.DB, covers CoverStore, batchSize int) *importer {
	return &importer{
		log:       log,
		db:        db,
		covers:    covers,
		batchSize: batchSize,
		coverPrio: map[int64]int{},
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
		// No open transaction: flush any pending cover ops accumulated without
		// an explicit begin (e.g. saveCoverIfBetter called after a prior commit).
		im.flushPendingCovers()
		return nil
	}
	err := im.tx.Commit()
	im.tx = nil
	im.queries = nil
	if err != nil {
		// On a failed commit the DB rows never persisted; discard the pending
		// cover ops so the caller's deferred rollback leaves disk clean.
		im.pendingCovers = nil
		return fmt.Errorf("commit import tx: %w", err)
	}
	im.flushPendingCovers()

	return nil
}

// flushPendingCovers replays all queued cover operations in order, then clears
// the queue. Called only after the DB transaction has successfully committed.
func (im *importer) flushPendingCovers() {
	for _, op := range im.pendingCovers {
		if op.data != nil {
			_ = im.covers.Save(op.bookID, op.data)
		} else {
			_ = im.covers.Delete(op.bookID)
		}
	}
	im.pendingCovers = nil
}

func (im *importer) rollback() {
	if im.tx != nil {
		_ = im.tx.Rollback()
		im.tx = nil
		im.queries = nil
	}
	// Discard any queued cover ops: nothing was written to disk during the
	// batch, so there is nothing to clean up.
	im.pendingCovers = nil
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

	matchKey, err := im.resolveMatchKey(ctx, q, rec)
	if err != nil {
		return 0, err
	}

	existing, err := q.FindBookByLibraryKey(ctx, dbq.FindBookByLibraryKeyParams{
		LibraryID: rec.LibraryID, LibraryKey: matchKey,
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

// resolveMatchKey returns the library_key to use when looking up an existing book.
// For derived-identity sources it runs an identifier pre-match first; for all
// others it returns rec.LibraryKey unchanged.
func (im *importer) resolveMatchKey(ctx context.Context, q *dbq.Queries, rec bookRecord) (string, error) {
	if !rec.DeriveIdentity {
		return rec.LibraryKey, nil
	}
	key, found, err := im.matchByIdentifier(ctx, q, rec)
	if err != nil {
		return "", err
	}
	if found {
		return key, nil
	}

	return rec.LibraryKey, nil
}

// matchByIdentifier finds an existing book in the record's library that shares one
// of the record's cleaned strong identifiers. When several match (e.g. ISBN and
// ASIN point at different already-split books), it returns the lowest book id's
// library_key so every file converges on the same winner within a sync.
func (im *importer) matchByIdentifier(
	ctx context.Context, q *dbq.Queries, rec bookRecord,
) (string, bool, error) {
	clean := cleanIdentifiers(rec.Identifiers)
	var (
		bestID            int64
		bestKey           string
		bestType, bestVal string
	)
	for typ, val := range clean {
		if _, ok := strongIdentifierTypes[typ]; !ok {
			continue
		}
		if !validStrongIdentifier(typ, val) {
			continue
		}
		row, err := q.FindBookByIdentifier(ctx, dbq.FindBookByIdentifierParams{
			LibraryID: rec.LibraryID, Type: typ, Value: val,
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("find book by identifier: %w", err)
		}
		if bestID == 0 || row.BookID < bestID {
			bestID, bestKey, bestType, bestVal = row.BookID, row.LibraryKey, typ, val
		}
	}

	if bestID != 0 && bestKey != rec.LibraryKey {
		im.log.Info(
			msgIdentifierOverride,
			slog.Int64("library_id", rec.LibraryID),
			slog.String("record_key", rec.LibraryKey),
			slog.Int64("matched_book", bestID),
			slog.String("matched_key", bestKey),
			slog.String("identifier_type", bestType),
			slog.String("identifier_value", bestVal),
		)
	}

	return bestKey, bestID != 0, nil
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
	im.pendingCovers = append(im.pendingCovers, coverOp{bookID: bookID, data: rec.Cover})
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
	im.pendingCovers = append(im.pendingCovers, coverOp{bookID: bookID})

	return nil
}
