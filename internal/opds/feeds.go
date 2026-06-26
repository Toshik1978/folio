package opds

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Toshik1978/folio/internal/bookfile"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

const defaultLimit = 50

// root handles GET /opds/ — the navigation feed linking to the catalog sections.
func (h *Handler) root(w http.ResponseWriter, _ *http.Request) {
	f := newFeed("urn:folio:opds:root", "Folio Library", opdsPrefix+"/", typeNavigation)
	f.Entries = []entry{
		navEntry("urn:folio:opds:all", "All Books", "Every book, newest first",
			opdsPrefix+"/search", typeAcquisition),
		navEntry("urn:folio:opds:authors", "By Author", "Browse authors",
			opdsPrefix+"/authors", typeNavigation),
		navEntry("urn:folio:opds:series", "By Series", "Browse series",
			opdsPrefix+"/series", typeNavigation),
		navEntry("urn:folio:opds:genres", "By Tag", "Browse tags",
			opdsPrefix+"/genres", typeNavigation),
	}
	h.write(w, typeNavigation, f)
}

// authors handles GET /opds/authors — a navigation feed of authors, each
// linking to that author's acquisition feed.
func (h *Handler) authors(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	page := pageParam(r)
	rows, err := h.q.ListAuthors(r.Context(), dbq.ListAuthorsParams{
		LibraryID: 0,
		Lim:       defaultLimit,
		Off:       (page - 1) * defaultLimit,
	})
	if err != nil {
		h.log.Error("opds: failed to list authors", slog.Any("error", err))
		http.Error(w, "Failed to List Authors", http.StatusInternalServerError)
		return
	}
	items := mapIndexItems(rows, func(a dbq.ListAuthorsRow) indexItem {
		return indexItem{id: a.ID, name: a.Name, count: a.BookCount}
	})
	h.writeIndexFeed(w, r, "urn:folio:opds:authors", "Authors by Name", opdsPrefix+"/authors", "author", items, page)
}

// series handles GET /opds/series — a navigation feed of series.
func (h *Handler) series(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	page := pageParam(r)
	rows, err := h.q.ListSeries(r.Context(), dbq.ListSeriesParams{
		LibraryID: 0,
		Lim:       defaultLimit,
		Off:       (page - 1) * defaultLimit,
	})
	if err != nil {
		h.log.Error("opds: failed to list series", slog.Any("error", err))
		http.Error(w, "Failed to List Series", http.StatusInternalServerError)
		return
	}
	items := mapIndexItems(rows, func(s dbq.ListSeriesRow) indexItem {
		return indexItem{id: s.ID, name: s.Name, count: s.BookCount}
	})
	h.writeIndexFeed(w, r, "urn:folio:opds:series", "Series by Name", opdsPrefix+"/series", "series", items, page)
}

// genres handles GET /opds/genres — a navigation feed of tags (genres), each
// linking to that tag's acquisition feed.
func (h *Handler) genres(w http.ResponseWriter, r *http.Request) { //nolint:dupl
	page := pageParam(r)
	rows, err := h.q.ListGenres(r.Context(), dbq.ListGenresParams{
		LibraryID: 0,
		Lim:       defaultLimit,
		Off:       (page - 1) * defaultLimit,
	})
	if err != nil {
		h.log.Error("opds: failed to list genres", slog.Any("error", err))
		http.Error(w, "Failed to List Genres", http.StatusInternalServerError)
		return
	}
	items := mapIndexItems(rows, func(g dbq.ListGenresRow) indexItem {
		return indexItem{id: g.ID, name: g.Name, count: g.BookCount}
	})
	h.writeIndexFeed(w, r, "urn:folio:opds:genres", "Tags by Name", opdsPrefix+"/genres", "tag", items, page)
}

// indexItem is one row of an author/series navigation feed.
type indexItem struct {
	id    int64
	name  string
	count int64
}

// mapIndexItems projects query rows into navigation index items.
func mapIndexItems[T any](rows []T, f func(T) indexItem) []indexItem {
	items := make([]indexItem, len(rows))
	for i := range rows {
		items[i] = f(rows[i])
	}
	return items
}

// writeIndexFeed renders a paginated navigation feed whose entries each link to
// a filtered acquisition feed (/opds/search?{param}={name}), with rel="next"/
// "previous" links when more pages exist.
func (h *Handler) writeIndexFeed(
	w http.ResponseWriter, r *http.Request, feedID, title, path, param string, items []indexItem, page int64,
) {
	f := newFeed(feedID, title, selfHref(r, path), typeNavigation)
	for i := range items {
		href := opdsPrefix + "/search?" + param + "=" + url.QueryEscape(items[i].name)
		f.Entries = append(f.Entries, navEntry(
			feedID+":"+strconv.FormatInt(items[i].id, 10),
			items[i].name, plural(items[i].count, "book", "books"), href, typeAcquisition,
		))
	}
	addPageLinks(&f, path, r.URL.Query(), page, int64(len(items)) == defaultLimit, typeNavigation)
	h.write(w, typeNavigation, f)
}

// search handles GET /opds/search — an acquisition feed. With no parameters it
// lists every book (newest first); otherwise it applies the q/author/series
// filters via the shared FilterBooks query (FTS5 BM25 when q is set).
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	qp := r.URL.Query()
	page := pageParam(r)
	// OPDS author/series feeds are exact browse navigation (the hrefs carry full
	// names), so they map to exact FieldFilters; q stays free-text FTS.
	filter := db.BookFilter{
		Query:  qp.Get("q"),
		Author: db.FieldFilter{Value: qp.Get("author"), Exact: qp.Get("author") != ""},
		Series: db.FieldFilter{Value: qp.Get("series"), Exact: qp.Get("series") != ""},
		Genre:  qp.Get("tag"), // exact tag/genre name (matches REST's ?tag=)
		Sort:   "source",      // OPDS "newest first" = source chronology, not Folio import order
		Limit:  defaultLimit,
		Offset: (page - 1) * defaultLimit,
	}

	books, err := db.FilterBooks(r.Context(), h.db, filter)
	if err != nil {
		h.log.Error("opds: failed to search books", slog.Any("error", err))
		http.Error(w, "Failed to Search Books", http.StatusInternalServerError)
		return
	}

	h.backfillPage(r.Context(), books) // offline-only, bounded and budgeted

	path := opdsPrefix + "/search"
	f := newFeed("urn:folio:opds:search", searchTitle(filter), selfHref(r, path), typeAcquisition)
	rel := h.fetchEntryRelations(r.Context(), books)
	for i := range books {
		f.Entries = append(f.Entries, h.bookEntry(books[i], rel))
	}
	addPageLinks(&f, path, qp, page, int64(len(books)) == defaultLimit, typeAcquisition)
	h.write(w, typeAcquisition, f)
}

// entryRelations carries a feed page's files/authors/genres keyed by book id,
// one query per relation instead of three per book (P1).
type entryRelations struct {
	files   map[int64][]dbq.BookFile
	authors map[int64][]string
	genres  map[int64][]string
}

// fetchEntryRelations is best-effort, like the per-book queries it replaces: a
// failed relation load renders entries without that relation, not a 500 feed.
func (h *Handler) fetchEntryRelations(ctx context.Context, books []dbq.Book) entryRelations {
	rel := entryRelations{
		files:   map[int64][]dbq.BookFile{},
		authors: map[int64][]string{},
		genres:  map[int64][]string{},
	}
	if len(books) == 0 {
		return rel
	}
	ids := make([]int64, 0, len(books))
	for i := range books {
		ids = append(ids, books[i].ID)
	}
	if files, err := h.q.ListFilesForBooks(ctx, ids); err == nil {
		for i := range files {
			rel.files[files[i].BookID] = append(rel.files[files[i].BookID], files[i])
		}
	}
	if authors, err := h.q.ListAuthorsForBooks(ctx, ids); err == nil {
		for i := range authors {
			rel.authors[authors[i].BookID] = append(rel.authors[authors[i].BookID], authors[i].Name)
		}
	}
	if genres, err := h.q.ListGenresForBooks(ctx, ids); err == nil {
		for i := range genres {
			rel.genres[genres[i].BookID] = append(rel.genres[genres[i].BookID], genres[i].Name)
		}
	}

	return rel
}

// bookEntry builds an acquisition entry (download + cover links, metadata) for
// one book from the page's pre-fetched relations.
func (h *Handler) bookEntry(b dbq.Book, rel entryRelations) entry {
	id := strconv.FormatInt(b.ID, 10)
	cv := b.ContentHash + "-" + h.covers.Version(b.ID)
	e := entry{
		Title:   b.Title,
		ID:      "urn:folio:book:" + id,
		Updated: time.Unix(b.AddedAt, 0).UTC().Format(time.RFC3339),
		Links: []link{
			{Rel: relImage, Href: opdsPrefix + "/books/" + id + "/cover?v=" + cv, Type: "image/jpeg"},
			{
				Rel:  relThumbnail,
				Href: opdsPrefix + "/books/" + id + "/cover/thumbnail?v=" + cv + "-" + h.covers.ThumbToken(),
				Type: "image/jpeg",
			},
		},
		Language: b.Language,
	}
	for _, file := range rel.files[b.ID] {
		e.Links = append(e.Links, link{
			Rel:  relAcquisition,
			Href: opdsPrefix + "/books/" + id + "/files/" + strconv.FormatInt(file.ID, 10),
			Type: bookfile.ContentType(file.FileFormat),
		})
	}
	if b.Publisher.Valid {
		e.Publisher = b.Publisher.String
	}
	if b.Year.Valid {
		e.Issued = strconv.FormatInt(b.Year.Int64, 10)
	}
	if b.Annotation.Valid {
		e.Content = &content{Type: "html", Value: h.annotationPolicy.Sanitize(b.Annotation.String)}
	}
	for _, name := range rel.authors[b.ID] {
		e.Authors = append(e.Authors, author{Name: name})
	}
	for _, name := range rel.genres[b.ID] {
		e.Categories = append(e.Categories, category{Term: name})
	}

	return e
}

// navEntry builds a navigation entry pointing at a sub-feed.
func navEntry(id, title, summary, href, mediaType string) entry {
	return entry{
		Title:   title,
		ID:      id,
		Updated: nowRFC3339(),
		Links:   []link{{Rel: relSubsection, Href: href, Type: mediaType}},
		Content: &content{Type: "text", Value: summary},
	}
}

func searchTitle(f db.BookFilter) string {
	switch {
	case f.Query != "":
		return "Search: " + f.Query
	case f.Author.Value != "":
		return "Books by " + f.Author.Value
	case f.Series.Value != "":
		return "Series: " + f.Series.Value
	case f.Genre != "":
		return "Tag: " + f.Genre
	default:
		return "All Books"
	}
}

func plural(n int64, one, many string) string { //nolint:unparam // That's OK we use it only for books at the moment
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
