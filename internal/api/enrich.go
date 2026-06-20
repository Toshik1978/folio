package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	dbf "github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// needsEnrichment reports whether a book still lacks key displayable metadata
// after the local tiers, making it a candidate for online enrichment. Every
// annotation-less book qualifies (not just PDFs) — see docs/NETWORKING.md for the
// egress this implies.
func (h *BooksHandler) needsEnrichment(book dbq.Book) bool {
	return !book.Annotation.Valid
}

// enrichOnline fills empty fields (and a missing cover) from the online source
// and marks the book enriched so it is queried at most once — even on a no-match
// (a negative cache). Best-effort: failures are logged, not fatal. A transient
// error (including a 429 cooldown) leaves the book unchecked so a later view
// retries. The network fetch and the DB persist run on separate budgets so a
// slow Google answer cannot exhaust the time left for the commit.
func (h *BooksHandler) enrichOnline(ctx context.Context, book *dbq.Book) {
	if h.enricher == nil {
		return
	}
	netCtx, cancel := context.WithTimeout(ctx, enrichTimeout)
	defer cancel()

	meta, ok, err := h.enricher.Enrich(netCtx, book.ID)
	if err != nil {
		h.log.Warn("online enrichment", slog.Int64("book", book.ID), slog.Any("error", err))
		return // transient → retry on next view
	}

	// The cover decision is filesystem work — HasLocalCover may parse the source
	// file (PDF render, INPX unzip) — so run it on the network tier's budget, not
	// the persist budget, so a slow parse can't starve the DB commit. A saved
	// cover is keyed by book id and survives a rolled-back persist for the retry.
	coverSaved := false
	if ok {
		coverSaved = h.saveEnrichedCover(netCtx, book.ID, meta.Cover)
	}

	// Persist on its own budget, detached from the request context: the network
	// spend is already sunk, so neither a depleted enrichTimeout nor a client
	// disconnect should roll back a commit that would just be re-fetched later.
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer pcancel()

	if ok {
		persisted, perr := h.applyEnrichment(pctx, book, meta, false, coverSaved)
		if perr != nil {
			h.log.Warn("persist enrichment", slog.Int64("book", book.ID), slog.Any("error", perr))
			return // tx rolled back → leave unchecked, retry later
		}
		if persisted {
			return // applyEnrichment persisted and marked the book enriched
		}
	}
	if err := h.q.MarkEnrichmentChecked(pctx, book.ID); err != nil {
		h.log.Warn("mark enrichment checked", slog.Int64("book", book.ID), slog.Any("error", err))
	}
}

// applyEnrichment persists enrichment metadata onto book. The cover is saved by
// the caller before this runs (on the network budget, not the persist budget),
// so the SQLite write lock is never held across that file I/O; coverSaved tells
// us a gap-filling cover landed. A single transaction then persists the scalar
// fields (annotation/publisher/year — gap-filled, or overwritten when overwrite
// is true), the identifiers, and the genres. In overwrite mode (manual Fix
// Match) it additionally replaces the most visible fields — title, authors, and
// series — and marks the book manually_matched so a later sync never reverts it,
// unconditionally, even when the chosen volume changed nothing displayable. It
// restamps content_hash so the ?v= cover cache-buster changes and marks the book
// enriched. Returns whether it persisted a displayable change, plus any error
// (the caller decides retry vs 5xx). Identifiers persist regardless of the change
// flag, so an identifier-only match never silently loses the ISBN / google volume
// id. The book pointer is mutated only after a successful commit, so a rolled-back
// transaction never leaks half-merged fields into the response the caller renders.
func (h *BooksHandler) applyEnrichment(
	ctx context.Context,
	book *dbq.Book,
	meta ebook.Metadata,
	overwrite bool,
	coverSaved bool,
) (bool, error) {
	b := *book // mutate a copy; publish to *book only on commit

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin enrichment tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	q := dbq.New(tx)

	changed, err := h.mergeEnrichmentChanges(ctx, q, &b, meta, overwrite)
	if err != nil {
		return false, err
	}
	if coverSaved {
		changed = true
	}

	if !changed {
		// Nothing displayable changed; identifiers may still have been written.
		// A Fix Match still locks the book: the manual marker (not the field
		// diff) is what keeps future syncs gap-fill-only, so it persists even
		// here. The caller marks the book enrichment-checked separately.
		if overwrite {
			if err := q.MarkManuallyMatched(ctx, b.ID); err != nil {
				return false, fmt.Errorf("mark manually matched: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit enrichment: %w", err)
		}

		return false, nil
	}

	b.ContentHash = newEnrichmentHash(b.ContentHash)
	if err := h.persistEnrichedScalars(ctx, q, &b, meta, overwrite); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit enrichment: %w", err)
	}
	*book = b

	return true, nil
}

// mergeEnrichmentChanges applies every in-place change for one enrichment to book
// and the DB via q — scalar fields, the overwrite-only author/series relinks,
// genres, and the identifiers — and reports whether anything displayable changed.
// The cover is saved by the caller before the transaction opens (see
// applyEnrichment). Identifiers persist unconditionally (before the caller's
// "nothing changed" gate) so an identifier-only match never loses the ISBN.
func (h *BooksHandler) mergeEnrichmentChanges(
	ctx context.Context,
	q *dbq.Queries,
	book *dbq.Book,
	meta ebook.Metadata,
	overwrite bool,
) (bool, error) {
	changed := h.mergeEnrichedFields(book, meta, overwrite)

	if overwrite {
		ch, err := h.applyOverwriteLinks(ctx, q, book, meta)
		if err != nil {
			return false, err
		}
		if ch {
			changed = true
		}
	}

	genreChanged, err := h.applyGenres(ctx, q, book.ID, meta.Genres, overwrite)
	if err != nil {
		return false, err
	}
	if genreChanged {
		changed = true
	}

	if err := persistIdentifiers(ctx, q, book.ID, meta.Identifiers, overwrite); err != nil {
		return false, err
	}

	return changed, nil
}

// mergeEnrichedFields sets annotation/publisher/year (and title in overwrite mode)
// on book from meta. In gap-fill mode (overwrite=false) only empty fields are
// filled; in overwrite mode any populated meta field replaces the book's. Returns
// whether anything changed.
func (h *BooksHandler) mergeEnrichedFields(book *dbq.Book, meta ebook.Metadata, overwrite bool) bool {
	changed := setNullString(&book.Annotation, meta.Annotation, strings.TrimSpace(meta.Annotation) != "", overwrite)
	if setNullString(&book.Publisher, meta.Publisher, meta.Publisher != "", overwrite) {
		changed = true
	}
	if setNullInt(&book.Year, int64(meta.Year), meta.Year != 0, overwrite) {
		changed = true
	}
	if overwrite && strings.TrimSpace(meta.Title) != "" && meta.Title != book.Title {
		book.Title = meta.Title
		changed = true
	}

	return changed
}

// setNullString writes val into dst when present and the field is fillable (empty,
// or overwrite mode). Returns whether it changed dst.
func setNullString(dst *sql.NullString, val string, present, overwrite bool) bool {
	if (overwrite || !dst.Valid) && present {
		*dst = sql.NullString{String: val, Valid: true}
		return true
	}

	return false
}

// setNullInt writes val into dst when present and the field is fillable (empty, or
// overwrite mode). Returns whether it changed dst.
func setNullInt(dst *sql.NullInt64, val int64, present, overwrite bool) bool {
	if (overwrite || !dst.Valid) && present {
		*dst = sql.NullInt64{Int64: val, Valid: true}
		return true
	}

	return false
}

// applyOverwriteLinks relinks a book's authors and series from a user-chosen
// volume (the manual Fix Match overwrite path). Authors are replaced only when the
// volume carries some (never blanked); series likewise. Returns whether anything
// changed.
func (h *BooksHandler) applyOverwriteLinks(
	ctx context.Context,
	q *dbq.Queries,
	book *dbq.Book,
	meta ebook.Metadata,
) (bool, error) {
	changed := false
	if len(meta.Authors) > 0 {
		if err := relinkAuthors(ctx, q, book.ID, meta.Authors); err != nil {
			return false, err
		}
		changed = true
	}
	if strings.TrimSpace(meta.Series) != "" {
		sid, err := q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: meta.Series, NameFold: dbf.Fold(meta.Series)})
		if err != nil {
			return false, fmt.Errorf("upsert series: %w", err)
		}
		book.SeriesID = sql.NullInt64{Int64: sid, Valid: true}
		if meta.SeriesNumber != 0 {
			book.SeriesNumber = sql.NullFloat64{Float64: meta.SeriesNumber, Valid: true}
		}
		changed = true
	}

	return changed, nil
}

// applyGenres persists the volume's genres: overwrite replaces them; the auto
// path gap-fills only when the book has none, so it never clobbers existing
// genres. Returns whether anything changed.
func (h *BooksHandler) applyGenres(
	ctx context.Context,
	q *dbq.Queries,
	bookID int64,
	genres []string,
	overwrite bool,
) (bool, error) {
	if len(genres) == 0 {
		return false, nil
	}
	if !overwrite {
		n, err := q.CountBookGenres(ctx, bookID)
		if err != nil {
			return false, fmt.Errorf("count genres: %w", err)
		}
		if n > 0 {
			return false, nil // gap-fill never clobbers existing genres
		}
	}
	if err := relinkGenres(ctx, q, bookID, genres, overwrite); err != nil {
		return false, err
	}

	return true, nil
}

// persistEnrichedScalars writes the merged scalar fields (and, in overwrite mode,
// title/series + the title/author/series FTS rows) and the annotation FTS row.
func (h *BooksHandler) persistEnrichedScalars(
	ctx context.Context,
	q *dbq.Queries,
	book *dbq.Book,
	meta ebook.Metadata,
	overwrite bool,
) error {
	if overwrite {
		if err := h.persistMatchScalars(ctx, q, book, meta); err != nil {
			return err
		}
	} else if err := q.UpdateBookEnrichment(ctx, dbq.UpdateBookEnrichmentParams{
		Annotation: book.Annotation, Publisher: book.Publisher, PublisherFold: dbf.FoldNull(book.Publisher),
		Year: book.Year, ContentHash: book.ContentHash, ID: book.ID,
	}); err != nil {
		return fmt.Errorf("persist enrichment: %w", err)
	}

	if book.Annotation.Valid {
		if err := q.UpdateBookFTSAnnotation(ctx, dbq.UpdateBookFTSAnnotationParams{
			Annotation: htmltext.StripMarkup(book.Annotation.String), BookID: itoa(book.ID),
		}); err != nil {
			return fmt.Errorf("enrichment fts: %w", err)
		}
	}

	return nil
}

// persistMatchScalars writes the overwrite (Fix Match) book row — title and series
// alongside the enrichment scalars — and keeps the title/author/series FTS rows in
// sync. Authors/series FTS are only touched when the volume carried them.
func (h *BooksHandler) persistMatchScalars(
	ctx context.Context,
	q *dbq.Queries,
	book *dbq.Book,
	meta ebook.Metadata,
) error {
	if err := q.UpdateBookMatch(ctx, dbq.UpdateBookMatchParams{
		Title: book.Title, SeriesID: book.SeriesID, SeriesNumber: book.SeriesNumber,
		Annotation: book.Annotation, Publisher: book.Publisher, PublisherFold: dbf.FoldNull(book.Publisher),
		Year: book.Year, ContentHash: book.ContentHash, ID: book.ID,
	}); err != nil {
		return fmt.Errorf("persist match: %w", err)
	}
	if err := q.UpdateBookFTSTitle(
		ctx,
		dbq.UpdateBookFTSTitleParams{Title: book.Title, BookID: itoa(book.ID)},
	); err != nil {
		return fmt.Errorf("match fts title: %w", err)
	}
	if len(meta.Authors) > 0 {
		if err := q.UpdateBookFTSAuthors(ctx, dbq.UpdateBookFTSAuthorsParams{
			Authors: strings.Join(meta.Authors, " "), BookID: itoa(book.ID),
		}); err != nil {
			return fmt.Errorf("match fts authors: %w", err)
		}
	}
	if strings.TrimSpace(meta.Series) != "" {
		if err := q.UpdateBookFTSSeries(
			ctx,
			dbq.UpdateBookFTSSeriesParams{Series: meta.Series, BookID: itoa(book.ID)},
		); err != nil {
			return fmt.Errorf("match fts series: %w", err)
		}
	}

	return nil
}

// relinkAuthors replaces a book's author links from names (the overwrite path),
// upserting each author by its case-fold key — the same single-sourced linking
// the importer uses.
func relinkAuthors(ctx context.Context, q *dbq.Queries, bookID int64, names []string) error {
	if err := q.DeleteBookAuthors(ctx, bookID); err != nil {
		return fmt.Errorf("clear authors: %w", err)
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		aid, err := q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: name, NameFold: dbf.Fold(name)})
		if err != nil {
			return fmt.Errorf("upsert author: %w", err)
		}
		if err := q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: bookID, AuthorID: aid}); err != nil {
			return fmt.Errorf("link author: %w", err)
		}
	}

	return nil
}

// relinkGenres links a book's genres from names. When replace is true the existing
// links are cleared first (overwrite); otherwise the inserts are additive (gap-fill).
func relinkGenres(ctx context.Context, q *dbq.Queries, bookID int64, names []string, replace bool) error {
	if replace {
		if err := q.DeleteBookGenres(ctx, bookID); err != nil {
			return fmt.Errorf("clear genres: %w", err)
		}
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		gid, err := q.InsertGenre(ctx, dbq.InsertGenreParams{Name: name, NameFold: dbf.Fold(name)})
		if err != nil {
			return fmt.Errorf("upsert genre: %w", err)
		}
		if err := q.InsertBookGenre(ctx, dbq.InsertBookGenreParams{BookID: bookID, GenreID: gid}); err != nil {
			return fmt.Errorf("link genre: %w", err)
		}
	}

	return nil
}

// persistIdentifiers writes cleaned identifiers via q. overwrite selects upsert
// (manual Fix Match — the user chose the volume, so values may refresh) vs
// insert-if-absent (auto enrichment / lazy backfill — gap-fill only, so an
// online ISBN-10 can never downgrade a sync-stored ISBN-13). Shared by the lazy
// backfill and online enrichment paths.
func persistIdentifiers(
	ctx context.Context,
	q *dbq.Queries,
	bookID int64,
	ids []ebook.Identifier,
	overwrite bool,
) error {
	for _, id := range ids {
		var err error
		if overwrite {
			err = q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
				BookID: bookID, Type: id.Type, Value: id.Value,
			})
		} else {
			err = q.InsertBookIdentifierIfAbsent(ctx, dbq.InsertBookIdentifierIfAbsentParams{
				BookID: bookID, Type: id.Type, Value: id.Value,
			})
		}
		if err != nil {
			return fmt.Errorf("persist identifier %s: %w", id.Type, err)
		}
	}

	return nil
}

// saveEnrichedCover caches an online cover, but only for a book that has no real
// local cover of its own. A locally-sourced cover (e.g. a PDF's page-1 render) is
// the true cover of the user's file and always beats an online thumbnail, so even
// a manual Fix Match never downgrades it — the online cover only fills a gap.
// Returns whether a cover was saved.
func (h *BooksHandler) saveEnrichedCover(ctx context.Context, bookID int64, cover []byte) bool {
	if len(cover) == 0 || h.coverSaver == nil {
		return false
	}
	if h.coverSaver.HasLocalCover(ctx, bookID) {
		return false
	}
	if err := h.coverSaver.Save(bookID, cover); err != nil {
		h.log.Warn("save enriched cover", slog.Int64("book", bookID), slog.Any("error", err))
		return false
	}

	return true
}

// newEnrichmentHash derives a fresh content_hash from the prior one so the cover
// cache-buster (?v=) changes after enrichment. It is intentionally not the value
// a sync would compute; the next sync detects the mismatch and restamps it to the
// canonical hash (a one-time, self-correcting refresh that preserves the
// gap-filled fields, which sync never overwrites with empties). Note: for an
// enriched row the canonical hash is computed from the index record *without* the
// enriched annotation, so content_hash stops being a true fingerprint of the
// displayed metadata for those rows (stable, no loop; see docs/DATABASE.md).
func newEnrichmentHash(prev string) string {
	sum := sha256.Sum256([]byte(prev + ":enriched"))

	return hex.EncodeToString(sum[:])
}
