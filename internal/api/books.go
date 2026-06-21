package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/bookfile"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// enrichTimeout bounds the whole online tier (lookup + cover fetch) on the
// user-facing getBook path, per RFC D5. The googlebooks client's own per-HTTP
// requestTimeout is a secondary bound, not the budget.
const enrichTimeout = 5 * time.Second

// persistTimeout bounds the local persistence of fetched enrichment separately
// from enrichTimeout: a slow Google answer must not leave the DB commit with no
// remaining budget, rolling back work that would only be re-fetched on the next
// view.
const persistTimeout = 3 * time.Second

// listBooks handles GET /api/books — a paginated, filtered list. When q is set
// results are ranked by FTS5 BM25 relevance; otherwise newest first.
func (h *BooksHandler) listBooks(w http.ResponseWriter, r *http.Request) {
	pageNo, limit, offset := pagination(r)
	qp := r.URL.Query()
	filter := db.BookFilter{
		Query:     strings.TrimSpace(qp.Get("q")),
		Title:     parseFieldFilter(qp.Get("title")),
		Author:    parseFieldFilter(qp.Get("author")),
		Series:    parseFieldFilter(qp.Get("series")),
		Genre:     qp.Get("tag"),
		Format:    qp.Get("format"),
		LibraryID: intQueryParam(r, "library"),
		Lang:      qp.Get("lang"),
		Publisher: qp.Get("publisher"),
		Sort:      qp.Get("sort"),
		Limit:     limit,
		Offset:    offset,
	}

	books, err := db.FilterBooks(r.Context(), h.db, filter)
	if err != nil {
		h.log.Error("list books", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to list books")
		return
	}
	total, err := db.CountFilteredBooks(r.Context(), h.db, filter)
	if err != nil {
		h.log.Error("count books", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to count books")
		return
	}

	ids := make([]int64, 0, len(books))
	for i := range books {
		ids = append(ids, books[i].ID)
	}
	rel, err := h.fetchBookRelations(r.Context(), ids)
	if err != nil {
		h.log.Error("load book relations", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to list books")
		return
	}

	sc := make(seriesCache)
	items := make([]bookView, 0, len(books))
	for i := range books {
		items = append(items, h.toBookView(r.Context(), books[i], sc, rel))
	}

	h.writeJSON(w, http.StatusOK, page[bookView]{Items: items, Total: total, Page: pageNo, Limit: limit})
}

// parseFieldFilter turns a facet query value into a db.FieldFilter. A leading
// '=' selects exact matching (e.g. "=Terry Pratchett"); otherwise the value is
// token-matched via FTS. Surrounding whitespace is trimmed.
func parseFieldFilter(raw string) db.FieldFilter {
	v := strings.TrimSpace(raw)
	if after, ok := strings.CutPrefix(v, "="); ok {
		return db.FieldFilter{Value: strings.TrimSpace(after), Exact: true}
	}
	return db.FieldFilter{Value: v}
}

// getBook handles GET /api/books/{id} — full detail for one book. For books
// missing an annotation it attempts a lazy backfill from the source file (e.g.
// INPX FB2) via the metadata extractor, persisting the result.
func (h *BooksHandler) getBook(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	book, err := h.q.GetBook(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		h.log.Error("get book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load book")
		return
	}

	h.runLazyTiers(r, id, &book)

	view, err := h.toSingleBookView(r.Context(), book)
	if err != nil {
		h.log.Error("render book", slog.Int64("book", id), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to render book")
		return
	}
	h.writeJSON(w, http.StatusOK, view)
}

// needsBackfill reports whether the lazy file-level backfill can still recover
// something: a missing annotation, or a book with no identifiers at all (common
// for Calibre rows that carry comments but no ISBN — without an ISBN the online
// tier falls back to fuzzy title search). The count query only runs while
// metadata_checked is 0, i.e. at most once per book.
func (h *BooksHandler) needsBackfill(ctx context.Context, book dbq.Book) bool {
	if !book.Annotation.Valid {
		return true
	}
	n, err := h.q.CountBookIdentifiers(ctx, book.ID)

	return err == nil && n == 0
}

// runLazyTiers executes the lazy write-on-read tiers for one book under the
// per-book claim. On a first view both tiers may parse the source file once
// each — Backfill here, then the extractor again inside saveEnrichedCover's
// HasLocalCover check. Both are guarded (once per book), and claimLazy keeps
// two concurrent first views from running them twice; the losing view serves
// the current row and the persisted result shows on a later view.
func (h *BooksHandler) runLazyTiers(r *http.Request, id int64, book *dbq.Book) {
	ctx := r.Context()
	backfill := book.MetadataChecked == 0 && h.needsBackfill(ctx, *book)
	enrich := book.EnrichmentChecked == 0 && h.needsEnrichment(*book)
	if !backfill && !enrich {
		return
	}
	if !h.claimLazy(id) {
		return
	}
	defer h.releaseLazy(id)

	if backfill {
		h.backfillMetadata(ctx, book)
	}
	// Re-evaluated after the backfill: a recovered annotation may have just
	// satisfied the enrichment trigger.
	if book.EnrichmentChecked == 0 && h.needsEnrichment(*book) {
		h.enrichOnline(ctx, book)
	}
}

// claimLazy marks a book's lazy write-on-read tiers (backfill + online
// enrichment) as in flight. It returns false when another request already runs
// them; that caller serves the current row and the persisted result shows on a
// later view — without this, two concurrent first views both hit Google Books
// and both run the merge.
func (h *BooksHandler) claimLazy(bookID int64) bool {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	if h.lazyInflight[bookID] {
		return false
	}
	h.lazyInflight[bookID] = true

	return true
}

func (h *BooksHandler) releaseLazy(bookID int64) {
	h.lazyMu.Lock()
	defer h.lazyMu.Unlock()
	delete(h.lazyInflight, bookID)
}

// backfillMetadata recovers metadata from a book's source file on first view —
// annotation and identifiers, which an INPX index carries for neither — persists
// what it finds, and marks the book checked so it is parsed at most once. The
// extractor skips PDFs, so a PDF-only book is marked without any parse.
// Best-effort: failures are logged, not fatal. A transient extractor error leaves
// the book unchecked so a later view retries; sync fills metadata directly on
// re-parse and never consults the checked flag.
func (h *BooksHandler) backfillMetadata(ctx context.Context, book *dbq.Book) {
	if h.extractor == nil {
		return
	}
	meta, ok, err := h.extractor.Backfill(ctx, book.ID)
	if err != nil {
		h.log.Warn("metadata backfill", slog.Int64("book", book.ID), slog.Any("error", err))
		return // transient → leave unchecked, retry on next view
	}
	if ok {
		if !book.Annotation.Valid {
			h.persistBackfilledAnnotation(ctx, book, meta.Annotation)
		}
		h.persistBackfilledIdentifiers(ctx, book.ID, meta.Identifiers)
	}
	if err := h.q.MarkMetadataChecked(ctx, book.ID); err != nil {
		h.log.Warn("mark metadata checked", slog.Int64("book", book.ID), slog.Any("error", err))
	}
}

// persistBackfilledAnnotation stores a recovered annotation on the book row and
// FTS index. A blank annotation is a no-op.
func (h *BooksHandler) persistBackfilledAnnotation(ctx context.Context, book *dbq.Book, annotation string) {
	if strings.TrimSpace(annotation) == "" {
		return
	}
	book.Annotation = sql.NullString{String: annotation, Valid: true}
	if err := h.q.UpdateBookAnnotation(ctx, dbq.UpdateBookAnnotationParams{
		Annotation: book.Annotation, ID: book.ID,
	}); err != nil {
		h.log.Warn("persist annotation", slog.Int64("book", book.ID), slog.Any("error", err))
		return
	}
	if err := h.q.UpdateBookFTSAnnotation(ctx, dbq.UpdateBookFTSAnnotationParams{
		Annotation: htmltext.StripMarkup(annotation), BookID: itoa(book.ID),
	}); err != nil {
		h.log.Warn("persist annotation fts", slog.Int64("book", book.ID), slog.Any("error", err))
	}
}

// persistBackfilledIdentifiers upserts the recovered identifiers (already cleaned
// by the extractor) via the shared, single-sourced identifier-write path. A
// failure is logged, not fatal — the backfill is best-effort.
func (h *BooksHandler) persistBackfilledIdentifiers(ctx context.Context, bookID int64, ids []ebook.Identifier) {
	if err := persistIdentifiers(ctx, h.q, bookID, ids, false); err != nil {
		h.log.Warn("persist identifier", slog.Int64("book", bookID), slog.Any("error", err))
	}
}

// downloadBook handles GET /api/books/{id}/files/{fileID} — streamed file download.
func (h *BooksHandler) downloadBook(w http.ResponseWriter, r *http.Request) {
	bookID, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	fileID, ok := intParam(r, "fileID")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}
	file, err := h.q.GetBookFile(r.Context(), fileID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && file.BookID != bookID) {
		h.writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		h.log.Error("get book file", slog.Int64("file", fileID), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load file")
		return
	}
	book, err := h.q.GetBook(r.Context(), bookID)
	if err != nil {
		h.log.Error("get book", slog.Int64("book", bookID), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load book")
		return
	}
	library, err := h.q.GetLibrary(r.Context(), book.LibraryID)
	if err != nil {
		h.log.Error("get library", slog.Int64("library", book.LibraryID), slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load library")
		return
	}

	if err := bookfile.Serve(w, r, library, file); err != nil {
		h.log.Error("serve book file", slog.Int64("file", fileID), slog.Any("error", err))
	}
}

// serveCover handles GET /api/books/{id}/cover, delegating to the cover server
// (cache → lazy extraction → placeholder). The {id} is validated to a positive
// integer, eliminating any path-traversal vector.
func (h *BooksHandler) serveCover(w http.ResponseWriter, r *http.Request) {
	id, ok := intParam(r, "id")
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid book id")
		return
	}
	h.covers.ServeHTTP(w, r, id)
}
