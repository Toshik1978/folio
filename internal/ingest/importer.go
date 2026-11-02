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

// Cover priorities stored in books.cover_prio besides the format ranks (the
// importer's injected rank) of covers saved at import.
const (
	// fileCoverPrio marks a cover the lazy flow extracted from the book's own
	// file (covers.Store.lazyExtract via CoverState.MarkFileCover).
	fileCoverPrio int64 = 1
	// manualCoverPrio pins a user-supplied cover; it matches api.manualCoverPrio.
	manualCoverPrio int64 = 1000
)

// msgIdentifierOverride is logged when identifier grouping links a record onto a
// book whose library_key differs from the record's own — i.e. strong-identifier
// matching overrode key-based grouping. This is the visibility hook for spotting
// a wrong auto-merge (reused/garbage ISBN), though it also fires on the intended
// author-order-drift heals; the attributes let a human tell them apart.
const msgIdentifierOverride = "identifier grouping overrode key-based grouping"

// coverOp is a deferred cover filesystem mutation: non-nil data means Save,
// nil data means Delete. Ops are queued during a batch and replayed on commit.
// prio, when > 0, is written to cover_prio only after the Save succeeds so the
// DB never claims a cover that isn't on disk. invalidate marks a Delete of a
// changed book's cover that also resets its cover state, so the lazy flow
// re-extracts it.
type coverOp struct {
	bookID     int64
	data       []byte // non-nil = Save; nil = Delete
	prio       int64  // when > 0 and Save succeeds, persist this as the book's cover_prio
	invalidate bool   // with nil data: also reset cover_state and cover_prio (ResetCoverState)
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
	// rank is the injected format preference (ebook.Dispatcher.Rank): higher
	// wins cover saves and metadata ownership; unknown formats rank 0.
	rank      func(format string) int
	coverPrio map[int64]int // bookID -> priority of the cover currently saved this run
	// pendingCovers holds cover filesystem mutations deferred until commit, so a
	// rolled-back batch never leaves a cover that disagrees with the DB.
	pendingCovers []coverOp
	count         int
}

// newImporter builds an importer with an initialized cover-priority tracker. The
// logger defaults to a discard handler so callers (and tests) that don't wire one
// stay silent; production wires the real logger via setLogger. rank orders the
// formats of a book's files (see importer.rank).
func newImporter(
	log *slog.Logger, db *sql.DB, covers CoverStore, batchSize int, rank func(format string) int,
) *importer {
	return &importer{
		log:       log,
		db:        db,
		covers:    covers,
		batchSize: batchSize,
		rank:      rank,
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

// flushPendingCovers replays queued cover operations in order after a successful
// commit: each Save/Delete, then — only for a cover that saved cleanly — the
// raised cover_prio. A Save failure is logged and its prio bump skipped, so the
// DB never claims a cover that isn't on disk. An invalidation deletes the cached
// cover, then resets the book's cover state. The sync
// engine holds the write guard across the whole run, so these post-commit writes
// are serialized.
func (im *importer) flushPendingCovers() {
	for _, op := range im.pendingCovers {
		if op.invalidate {
			im.invalidateCover(op.bookID)
			continue
		}
		if op.data == nil {
			_ = im.covers.Delete(op.bookID)
			continue
		}
		if err := im.covers.Save(op.bookID, op.data); err != nil {
			im.log.Warn("save cover", slog.Int64("book", op.bookID), slog.Any("error", err))
			continue
		}
		if op.prio > 0 {
			err := dbq.New(im.db).UpdateBookCoverPrio(context.Background(),
				dbq.UpdateBookCoverPrioParams{CoverPrio: op.prio, ID: op.bookID})
			if err != nil {
				im.log.Warn("persist cover prio", slog.Int64("book", op.bookID), slog.Any("error", err))
			}
		}
	}
	im.pendingCovers = nil
}

// invalidateCover drops bookID's cached cover and resets its cover state, so
// the lazy flow re-extracts it from the changed file. add never queues an
// invalidation for a manual cover (its cover_prio outranks every format), and
// ResetCoverState guards the row the same way.
func (im *importer) invalidateCover(bookID int64) {
	if err := im.covers.Delete(bookID); err != nil {
		im.log.Warn("delete invalidated cover", slog.Int64("book", bookID), slog.Any("error", err))
		return
	}
	if err := dbq.New(im.db).ResetCoverState(context.Background(), bookID); err != nil {
		im.log.Warn("reset cover state", slog.Int64("book", bookID), slog.Any("error", err))
	}
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
// outranks any already saved. changed reports that rec's file is new or its
// size or mod time moved; on an existing book it, like a file joining the book
// (even with unchanged bytes), may invalidate the book's file-derived cover
// (see invalidateCoverIfChanged) and drops its read-failure records.
func (im *importer) add(ctx context.Context, rec bookRecord, addedAt int64, changed bool) (int64, error) {
	b, err := im.upsertBook(ctx, rec, addedAt)
	if err != nil {
		return 0, err
	}

	im.saveCoverIfBetter(ctx, b.id, b.coverPrio, rec)
	if (changed || b.joined) && !b.inserted { // a brand-new book has no cover or failure records
		im.invalidateCoverIfChanged(b.id, b.coverState, b.coverPrio, rec)
		if err := im.dropReadFailures(ctx, b.id); err != nil {
			return 0, err
		}
	}

	im.count++
	if im.count >= im.batchSize {
		if err := im.commit(); err != nil {
			return 0, fmt.Errorf("commit batch: %w", err)
		}
		im.count = 0
	}

	return b.id, nil
}

// dropReadFailures deletes bookID's remembered read failures in the import
// transaction: a changed file may now be readable, so the next read must open
// it rather than trust a record of the old bytes.
func (im *importer) dropReadFailures(ctx context.Context, bookID int64) error {
	q, err := im.getQueries(ctx)
	if err != nil {
		return err
	}
	if err := q.DeleteReadFailuresForBook(ctx, bookID); err != nil {
		return fmt.Errorf("delete read failures: %w", err)
	}

	return nil
}

// upsertedBook is the logical book add persisted a record onto, with the cover
// state and prio it had before this record (zero for a book just inserted).
// joined reports that the record's file was new to an existing book.
type upsertedBook struct {
	id         int64
	coverState int64
	coverPrio  int64
	inserted   bool
	joined     bool
}

// upsertBook groups rec onto the existing logical book for its match key,
// merging rec's metadata into it, or inserts a new book.
func (im *importer) upsertBook(ctx context.Context, rec bookRecord, addedAt int64) (upsertedBook, error) {
	q, err := im.getQueries(ctx)
	if err != nil {
		return upsertedBook{}, err
	}

	matchKey, err := im.resolveMatchKey(ctx, q, rec)
	if err != nil {
		return upsertedBook{}, err
	}

	existing, err := q.FindBookByLibraryKey(ctx, dbq.FindBookByLibraryKeyParams{
		LibraryID: rec.LibraryID, LibraryKey: matchKey,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, insErr := insertBook(ctx, q, im.tx, rec, resolveAddedAt(rec, addedAt), addedAt)
		if insErr != nil {
			return upsertedBook{}, insErr
		}

		return upsertedBook{id: id, inserted: true}, nil
	case err != nil:
		return upsertedBook{}, fmt.Errorf("find book by key: %w", err)
	}
	joined, err := mergeExisting(ctx, q, im.tx, existing, rec, im.rank)
	if err != nil {
		return upsertedBook{}, err
	}

	return upsertedBook{
		id: existing.ID, coverState: existing.CoverState, coverPrio: existing.CoverPrio, joined: joined,
	}, nil
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
// bytes may have changed, and covers.Save skips identical content. The priority
// bump is deferred to flushPendingCovers and applied only when the Save succeeds,
// so the DB never claims a cover that isn't on disk.
func (im *importer) saveCoverIfBetter(_ context.Context, bookID, persistedPrio int64, rec bookRecord) {
	if len(rec.Cover) == 0 {
		return
	}
	p := int64(im.rank(rec.FileFormat))
	if p < persistedPrio {
		return
	}
	if cur, ok := im.coverPrio[bookID]; ok && int(p) <= cur {
		return
	}
	im.coverPrio[bookID] = int(p)
	op := coverOp{bookID: bookID, data: rec.Cover}
	if p > persistedPrio {
		op.prio = p
	}
	im.pendingCovers = append(im.pendingCovers, op)
}

// invalidateCoverIfChanged queues an invalidation of an existing book's cover
// after one of its files changed (or a new file joined it), so the lazy flow
// re-extracts the cover from the new file. Only state the file produced is
// invalidated: a none marker (cover_state 2), or a cached cover (cover_state 1)
// whose prio marks it file-derived (fileCoverPrio up to below manualCoverPrio:
// lazily extracted or saved at import). A cover with prio 0 (online
// enrichment, nothing would fetch it again) and a manual cover are kept. It is
// also skipped when rec brings cover bytes of its own, when persistedPrio
// outranks rec's format (a richer edition's cover), and when a cover was
// already saved for the book this run (a Calibre cover.jpg arrives on only the
// first of a book's format rows). Like every cover op it runs only after the
// batch commits.
func (im *importer) invalidateCoverIfChanged(bookID, coverState, persistedPrio int64, rec bookRecord) {
	if len(rec.Cover) > 0 || int64(im.rank(rec.FileFormat)) < persistedPrio {
		return
	}
	im.invalidateFileCover(bookID, coverState, persistedPrio)
}

// invalidateFileCover queues an invalidation of bookID's cover when its state
// is file-produced (see invalidateCoverIfChanged) and no cover was saved for
// the book this run.
func (im *importer) invalidateFileCover(bookID, coverState, persistedPrio int64) {
	fileState := coverState == coverStateNone ||
		(coverState == coverStateHas && persistedPrio >= fileCoverPrio && persistedPrio < manualCoverPrio)
	if !fileState {
		return
	}
	if _, saved := im.coverPrio[bookID]; saved {
		return
	}
	im.pendingCovers = append(im.pendingCovers, coverOp{bookID: bookID, invalidate: true})
}

// fileLeft resets what bookID derived from a file that moved to another book
// (its grouping key changed): a file-derived cover or none marker may describe
// the file it no longer has, and a read-failure record may be about that file.
// A book left with no files is pruned later anyway; the reset is harmless.
func (im *importer) fileLeft(ctx context.Context, bookID int64) error {
	q, err := im.getQueries(ctx)
	if err != nil {
		return err
	}
	book, err := q.GetBook(ctx, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load book %d: %w", bookID, err)
	}
	im.invalidateFileCover(bookID, book.CoverState, book.CoverPrio)

	return im.dropReadFailures(ctx, bookID)
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
