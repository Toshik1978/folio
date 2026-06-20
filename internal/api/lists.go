package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// nameCountView is the JSON shape for tag and publisher list entries (no id):
// it mirrors web/src/types.ts Tag and Publisher.
type nameCountView struct {
	Name      string `json:"name"`
	BookCount int64  `json:"book_count"`
}

// nameIDCountView is the JSON shape for series list entries: id + name +
// book_count. It mirrors web/src/types.ts Series and avoids overloading
// authorView (which carries the same fields but reads as author-specific).
type nameIDCountView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BookCount int64  `json:"book_count"`
}

// The browse endpoints come in pairs. GET /api/<entity>/letters returns the
// alphabet buckets that have data (driving the selector); GET /api/<entity>
// returns one bucket's entries, paginated (?letter=, ?page=, ?limit=). Loading
// a letter at a time keeps these fast on a large library where the old
// load-everything queries returned 100k+ rows. See letters.go for bucketing.

// writeLetters runs an entity's *FirstChars query and writes the set of
// available alphabet buckets, in display order.
func (h *CatalogHandler) writeLetters(
	w http.ResponseWriter, r *http.Request, label string,
	fetch func(context.Context, int64) ([]string, error),
) {
	chars, err := fetch(r.Context(), intQueryParam(r, "library"))
	if err != nil {
		h.log.Error("list "+label+" letters", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to list "+label+" letters")
		return
	}
	h.writeJSON(w, http.StatusOK, availableLetters(chars))
}

// browseByLetter is the shared body of the by-letter browse endpoints. It picks
// the query for the selected bucket — the name-range query for a normal letter,
// or the '#' catch-all — maps the rows to their JSON view, and writes them. An
// empty (or unknown) letter yields an empty array; the frontend always selects
// a letter first. ByRow and NonRow are the two distinct sqlc row types the two
// queries return, each with its own mapper.
func browseByLetter[ByRow, NonRow, View any](
	h *CatalogHandler, w http.ResponseWriter, r *http.Request, label string,
	byLetter func(ctx context.Context, lib int64, lo, hi string, lim, off int64) ([]ByRow, error),
	convBy func(ByRow) View,
	nonLetter func(ctx context.Context, lib, lim, off int64) ([]NonRow, error),
	convNon func(NonRow) View,
) {
	letter := r.URL.Query().Get("letter")
	if letter == "" {
		h.writeJSON(w, http.StatusOK, []View{})
		return
	}
	_, limit, offset := pagination(r)
	lib := intQueryParam(r, "library")
	ctx := r.Context()

	var views []View
	var err error
	if letter == hashBucket {
		var rows []NonRow
		rows, err = nonLetter(ctx, lib, limit, offset)
		views = mapViews(rows, convNon)
	} else if lo, hi, ok := letterBounds(letter); ok {
		var rows []ByRow
		rows, err = byLetter(ctx, lib, lo, hi, limit, offset)
		views = mapViews(rows, convBy)
	}
	if err != nil {
		h.log.Error("list "+label, slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to list "+label)
		return
	}
	h.writeJSON(w, http.StatusOK, views)
}

// mapViews maps query rows to their JSON view, always returning a non-nil slice
// so empty results serialize as [] rather than null.
func mapViews[Row, View any](rows []Row, conv func(Row) View) []View {
	views := make([]View, 0, len(rows))
	for i := range rows {
		views = append(views, conv(rows[i]))
	}
	return views
}

func (h *CatalogHandler) authorLetters(w http.ResponseWriter, r *http.Request) {
	h.writeLetters(w, r, "authors", h.q.AuthorFirstChars)
}

func (h *CatalogHandler) seriesLetters(w http.ResponseWriter, r *http.Request) {
	h.writeLetters(w, r, "series", h.q.SeriesFirstChars)
}

func (h *CatalogHandler) tagLetters(w http.ResponseWriter, r *http.Request) {
	h.writeLetters(w, r, "tags", h.q.GenreFirstChars)
}

func (h *CatalogHandler) publisherLetters(w http.ResponseWriter, r *http.Request) {
	h.writeLetters(w, r, "publishers", h.q.PublisherFirstChars)
}

// listAuthors handles GET /api/authors?letter=&page=&limit= — one alphabet
// bucket of authors with book counts (web/src/types.ts Author).
func (h *CatalogHandler) listAuthors(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	browseByLetter(
		h, w, r, "authors",
		func(ctx context.Context, lib int64, lo, hi string, lim, off int64) ([]dbq.ListAuthorsByLetterRow, error) {
			return h.q.ListAuthorsByLetter(
				ctx,
				dbq.ListAuthorsByLetterParams{LibraryID: lib, Lo: lo, Hi: hi, Lim: lim, Off: off},
			)
		},
		func(a dbq.ListAuthorsByLetterRow) authorView {
			return authorView{ID: a.ID, Name: a.Name, BookCount: a.BookCount}
		},
		func(ctx context.Context, lib, lim, off int64) ([]dbq.ListAuthorsNonLetterRow, error) {
			return h.q.ListAuthorsNonLetter(ctx, dbq.ListAuthorsNonLetterParams{LibraryID: lib, Lim: lim, Off: off})
		},
		func(a dbq.ListAuthorsNonLetterRow) authorView {
			return authorView{ID: a.ID, Name: a.Name, BookCount: a.BookCount}
		},
	)
}

// listSeries handles GET /api/series?letter=&page=&limit= (web/src/types.ts Series).
func (h *CatalogHandler) listSeries(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	browseByLetter(
		h, w, r, "series",
		func(ctx context.Context, lib int64, lo, hi string, lim, off int64) ([]dbq.ListSeriesByLetterRow, error) {
			return h.q.ListSeriesByLetter(
				ctx,
				dbq.ListSeriesByLetterParams{LibraryID: lib, Lo: lo, Hi: hi, Lim: lim, Off: off},
			)
		},
		func(s dbq.ListSeriesByLetterRow) nameIDCountView {
			return nameIDCountView{ID: s.ID, Name: s.Name, BookCount: s.BookCount}
		},
		func(ctx context.Context, lib, lim, off int64) ([]dbq.ListSeriesNonLetterRow, error) {
			return h.q.ListSeriesNonLetter(ctx, dbq.ListSeriesNonLetterParams{LibraryID: lib, Lim: lim, Off: off})
		},
		func(s dbq.ListSeriesNonLetterRow) nameIDCountView {
			return nameIDCountView{ID: s.ID, Name: s.Name, BookCount: s.BookCount}
		},
	)
}

// listTags handles GET /api/tags?letter=&page=&limit= (web/src/types.ts Tag).
func (h *CatalogHandler) listTags(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	browseByLetter(
		h, w, r, "tags",
		func(ctx context.Context, lib int64, lo, hi string, lim, off int64) ([]dbq.ListGenresByLetterRow, error) {
			return h.q.ListGenresByLetter(
				ctx,
				dbq.ListGenresByLetterParams{LibraryID: lib, Lo: lo, Hi: hi, Lim: lim, Off: off},
			)
		},
		func(g dbq.ListGenresByLetterRow) nameCountView {
			return nameCountView{Name: g.Name, BookCount: g.BookCount}
		},
		func(ctx context.Context, lib, lim, off int64) ([]dbq.ListGenresNonLetterRow, error) {
			return h.q.ListGenresNonLetter(ctx, dbq.ListGenresNonLetterParams{LibraryID: lib, Lim: lim, Off: off})
		},
		func(g dbq.ListGenresNonLetterRow) nameCountView {
			return nameCountView{Name: g.Name, BookCount: g.BookCount}
		},
	)
}

// listPublishers handles GET /api/publishers?letter=&page=&limit=
// (web/src/types.ts Publisher).
func (h *CatalogHandler) listPublishers(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	browseByLetter(
		h, w, r, "publishers",
		func(ctx context.Context, lib int64, lo, hi string, lim, off int64) ([]dbq.ListPublishersByLetterRow, error) {
			return h.q.ListPublishersByLetter(
				ctx,
				dbq.ListPublishersByLetterParams{LibraryID: lib, Lo: lo, Hi: hi, Lim: lim, Off: off},
			)
		},
		func(p dbq.ListPublishersByLetterRow) nameCountView {
			return nameCountView{Name: p.Name, BookCount: p.BookCount}
		},
		func(ctx context.Context, lib, lim, off int64) ([]dbq.ListPublishersNonLetterRow, error) {
			return h.q.ListPublishersNonLetter(
				ctx,
				dbq.ListPublishersNonLetterParams{LibraryID: lib, Lim: lim, Off: off},
			)
		},
		func(p dbq.ListPublishersNonLetterRow) nameCountView {
			return nameCountView{Name: p.Name, BookCount: p.BookCount}
		},
	)
}
