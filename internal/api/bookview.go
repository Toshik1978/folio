package api

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/microcosm-cc/bluemonday"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// annotationPolicy sanitizes stored annotation HTML before it is served, so the
// frontend can render it via v-html without an XSS risk. UGCPolicy permits
// common formatting tags (p, em, strong, lists, links, …) and strips scripts,
// event handlers, and other dangerous markup. Sanitizing here, at the serve
// boundary, covers every library and any already-stored data.
var annotationPolicy = bluemonday.UGCPolicy()

// bookView is the JSON shape consumed by the frontend (web/src/types.ts Book).
type bookView struct {
	ID          int64            `json:"id"`
	Title       string           `json:"title"`
	Authors     []bookAuthorView `json:"authors"`
	Series      *string          `json:"series"`
	SeriesIndex *float64         `json:"series_index"`
	Tags        []string         `json:"tags"`
	Publisher   *string          `json:"publisher"`
	Year        *int             `json:"year"`
	Pages       *int             `json:"pages"`
	Rating      *int             `json:"rating"`
	Language    *string          `json:"language"`
	Annotation  *string          `json:"annotation"`
	Formats     []formatView     `json:"formats"`
	Identifiers []identifierView `json:"identifiers"`
	CoverURL    *string          `json:"cover_url"`
}

type authorView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BookCount int64  `json:"book_count"`
}

// bookAuthorView is the author shape embedded in a book (detail/list). Unlike
// authorView it omits book_count, which is meaningless in a book context — the
// browse-list count would always serialize as 0 here.
type bookAuthorView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type formatView struct {
	Type        string `json:"type"`
	SizeBytes   int64  `json:"size_bytes"`
	DownloadURL string `json:"download_url"`
}

type identifierView struct {
	Type  string  `json:"type"`
	Value string  `json:"value"`
	URL   *string `json:"url"`
}

// seriesCache memoises series-name lookups within a single request so a page of
// books from the same series triggers one query, not one per book.
type seriesCache map[int64]string

func (c seriesCache) name(ctx context.Context, q *dbq.Queries, id int64) (string, error) {
	if name, ok := c[id]; ok {
		return name, nil
	}
	s, err := q.GetSeries(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get series %d: %w", id, err)
	}
	c[id] = s.Name

	return s.Name, nil
}

// bookRelations holds one page's relation rows keyed by book id, fetched with
// one query per relation (4 total) instead of 4 per book (P1).
type bookRelations struct {
	authors map[int64][]bookAuthorView
	tags    map[int64][]string
	idents  map[int64][]identifierView
	files   map[int64][]dbq.BookFile
}

// fetchBookRelations loads the relation maps for ids in four statements.
func (h *BooksHandler) fetchBookRelations(ctx context.Context, ids []int64) (bookRelations, error) {
	rel := bookRelations{
		authors: make(map[int64][]bookAuthorView, len(ids)),
		tags:    make(map[int64][]string, len(ids)),
		idents:  make(map[int64][]identifierView, len(ids)),
		files:   make(map[int64][]dbq.BookFile, len(ids)),
	}
	if len(ids) == 0 {
		return rel, nil
	}
	if err := h.loadAuthorGenreRelations(ctx, ids, &rel); err != nil {
		return rel, err
	}
	if err := h.loadFileIdentifierRelations(ctx, ids, &rel); err != nil {
		return rel, err
	}

	return rel, nil
}

func (h *BooksHandler) loadAuthorGenreRelations(ctx context.Context, ids []int64, rel *bookRelations) error {
	authors, err := h.q.ListAuthorsForBooks(ctx, ids)
	if err != nil {
		return fmt.Errorf("authors for books: %w", err)
	}
	for i := range authors {
		rel.authors[authors[i].BookID] = append(rel.authors[authors[i].BookID],
			bookAuthorView{ID: authors[i].ID, Name: authors[i].Name})
	}
	genres, err := h.q.ListGenresForBooks(ctx, ids)
	if err != nil {
		return fmt.Errorf("genres for books: %w", err)
	}
	for i := range genres {
		rel.tags[genres[i].BookID] = append(rel.tags[genres[i].BookID], genres[i].Name)
	}

	return nil
}

func (h *BooksHandler) loadFileIdentifierRelations(ctx context.Context, ids []int64, rel *bookRelations) error {
	idents, err := h.q.ListIdentifiersForBooks(ctx, ids)
	if err != nil {
		return fmt.Errorf("identifiers for books: %w", err)
	}
	for i := range idents {
		rel.idents[idents[i].BookID] = append(rel.idents[idents[i].BookID], identifierView{
			Type: idents[i].Type, Value: idents[i].Value, URL: identifierURL(idents[i].Type, idents[i].Value),
		})
	}
	files, err := h.q.ListFilesForBooks(ctx, ids)
	if err != nil {
		return fmt.Errorf("files for books: %w", err)
	}
	for i := range files {
		rel.files[files[i].BookID] = append(rel.files[files[i].BookID], files[i])
	}

	return nil
}

// toBookView renders the JSON view of b from the pre-fetched page relations.
// Series resolution is best-effort (resolveSeries swallows lookup errors rather
// than failing the whole view), so this never reports an error.
func (h *BooksHandler) toBookView(
	ctx context.Context,
	b dbq.Book,
	sc seriesCache,
	rel bookRelations,
) bookView {
	av := rel.authors[b.ID]
	if av == nil {
		av = make([]bookAuthorView, 0)
	}
	tags := rel.tags[b.ID]
	if tags == nil {
		tags = make([]string, 0)
	}
	identifiers := rel.idents[b.ID]
	if identifiers == nil {
		identifiers = make([]identifierView, 0)
	}
	formats, pages := bookFormats(b.ID, rel.files[b.ID])

	lang := b.Language
	// ?v= combines the metadata hash (cover selection) with the cover file
	// mtime (cover byte changes without a metadata change) — see covers.Store.Version.
	cover := fmt.Sprintf("/api/books/%d/cover?v=%s-%s", b.ID, b.ContentHash, h.covers.Version(b.ID))
	view := bookView{
		ID:          b.ID,
		Title:       b.Title,
		Authors:     av,
		Tags:        tags,
		Language:    &lang,
		Publisher:   nullStr(b.Publisher),
		Year:        nullIntToPtr(b.Year),
		Pages:       pages,
		Rating:      nullIntToPtr(b.Rating),
		Formats:     formats,
		Identifiers: identifiers,
		CoverURL:    &cover,
	}
	if b.Annotation.Valid {
		clean := annotationPolicy.Sanitize(b.Annotation.String)
		view.Annotation = &clean
	}
	h.resolveSeries(ctx, b, sc, &view)

	return view
}

// nullIntToPtr converts a nullable integer column to *int (nil when absent).
func nullIntToPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// toSingleBookView renders one book, fetching its relations itself (the
// page-batched path is listBooks).
func (h *BooksHandler) toSingleBookView(ctx context.Context, b dbq.Book) (bookView, error) {
	rel, err := h.fetchBookRelations(ctx, []int64{b.ID})
	if err != nil {
		return bookView{}, err
	}

	return h.toBookView(ctx, b, make(seriesCache), rel), nil
}

// resolveSeries fills the view's series name and index from the book row.
func (h *BooksHandler) resolveSeries(ctx context.Context, b dbq.Book, sc seriesCache, view *bookView) {
	if b.SeriesID.Valid {
		if name, err := sc.name(ctx, h.q, b.SeriesID.Int64); err == nil {
			view.Series = &name
		}
	}
	if b.SeriesNumber.Valid {
		view.SeriesIndex = &b.SeriesNumber.Float64
	}
}

// bookFormats turns a book's files into format views (one downloadable entry
// per file) and returns the first non-null page count across them.
func bookFormats(bookID int64, files []dbq.BookFile) ([]formatView, *int) {
	formats := make([]formatView, 0, len(files))
	var pages *int
	for i := range files {
		formats = append(formats, formatView{
			Type:        files[i].FileFormat,
			SizeBytes:   files[i].FileSize,
			DownloadURL: fmt.Sprintf("/api/books/%d/files/%d", bookID, files[i].ID),
		})
		if pages == nil && files[i].Pages.Valid {
			p := int(files[i].Pages.Int64)
			pages = &p
		}
	}

	return formats, pages
}

// identifierURL returns a canonical link for known identifier providers, or nil.
func identifierURL(typ, value string) *string {
	var url string
	switch typ {
	case "amazon":
		url = "https://www.amazon.com/dp/" + value
	case "goodreads":
		url = "https://www.goodreads.com/book/show/" + value
	case "google":
		url = "https://books.google.com/books?id=" + value
	case "isbn":
		url = "https://isbnsearch.org/isbn/" + value
	default:
		return nil
	}

	return &url
}
