package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// CoverSearcher aggregates cover candidates across online providers. It is
// optional: a nil searcher disables in-app cover search (manual upload/URL
// remain). *metasearch.Aggregator satisfies it.
type CoverSearcher interface {
	SearchCovers(ctx context.Context, q metasearch.Query) []metasearch.CoverCandidate
}

// searchCovers handles GET /api/books/{id}/cover/search?q= — aggregated cover
// candidates the user can apply via POST /books/{id}/cover. The query is seeded
// from the book (title + first author + ISBN) unless ?q= overrides the title.
func (h *BooksHandler) searchCovers(w http.ResponseWriter, r *http.Request) {
	if h.coverSearch == nil {
		h.writeError(w, http.StatusNotImplemented, "cover search disabled")
		return
	}
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

	q := h.seedQuery(r.Context(), book)
	if explicit := strings.TrimSpace(r.URL.Query().Get("q")); explicit != "" {
		// An explicit query overrides the title and drops the stored author (a
		// publisher-as-author can zero out provider results). The ISBN and ASIN
		// are kept: they are exact ids, never narrow wrongly, and are the keys
		// that yield the correct edition's cover (OpenLibrary by ISBN, Amazon by
		// ASIN product page).
		q.Title = explicit
		q.Author = ""
	}

	candidates := h.coverSearch.SearchCovers(r.Context(), q)
	h.writeJSON(w, http.StatusOK, candidates)
}

// seedQuery builds a provider query from a book: its title, first author, and
// first ISBN identifier when present. Errors loading authors/identifiers are
// non-fatal — a thinner query is still useful.
func (h *BooksHandler) seedQuery(ctx context.Context, book dbq.Book) metasearch.Query {
	q := metasearch.Query{Title: book.Title}

	if authors, err := h.q.ListAuthorsForBook(ctx, book.ID); err != nil {
		h.log.Warn("seed query authors", slog.Int64("book", book.ID), slog.Any("error", err))
	} else if len(authors) > 0 {
		q.Author = authors[0].Name
	}

	if ids, err := h.q.ListIdentifiersForBook(ctx, book.ID); err != nil {
		h.log.Warn("seed query identifiers", slog.Int64("book", book.ID), slog.Any("error", err))
	} else {
		ingest.ApplyIdentifierQuery(&q, ids)
	}

	return q
}
