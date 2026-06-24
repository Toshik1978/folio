package api

import (
	"archive/zip"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/ebook"
)

type booksSuite struct {
	baseSuite
}

func (s *booksSuite) TestListBooksPaginated() {
	src := s.seedLibrary("folder", "/lib")
	for i := range 30 {
		s.seedBook(src, bookSeed{Title: "Book " + itoa(int64(i)), Authors: []string{"Asimov"}})
	}

	w := s.do(http.MethodGet, "/books?page=1&limit=10", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var resp page[bookView]
	s.decode(w, &resp)
	s.Equal(int64(30), resp.Total)
	s.Equal(int64(1), resp.Page)
	s.Equal(int64(10), resp.Limit)
	s.Len(resp.Items, 10)

	first := resp.Items[0]
	s.NotEmpty(first.Title)
	s.Len(first.Authors, 1)
	s.Equal("Asimov", first.Authors[0].Name)
	s.Len(first.Formats, 1)
	s.Equal("/api/books/"+itoa(first.ID)+"/files/"+itoa(s.firstFileID(first.ID)), first.Formats[0].DownloadURL)
	s.NotNil(first.CoverURL)
}

func (s *booksSuite) TestGetBookIncludesRating() {
	src := s.seedLibrary("folder", "/lib")
	// Annotation present so the lazy backfill path is skipped entirely.
	id := s.seedBook(src, bookSeed{Title: "Rated", Annotation: "x", Rating: 4})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var bv bookView
	s.decode(w, &bv)
	s.Require().NotNil(bv.Rating)
	s.Equal(4, *bv.Rating)
}

func (s *booksSuite) TestListBooksFilterByAuthorAndFormat() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Format: "epub", Authors: []string{"Asimov"}})
	s.seedBook(src, bookSeed{Title: "Dune", Format: "fb2", Authors: []string{"Herbert"}})

	w := s.do(http.MethodGet, "/books?author=Asimov", nil)
	var byAuthor page[bookView]
	s.decode(w, &byAuthor)
	s.Equal(int64(1), byAuthor.Total)
	s.Equal("Foundation", byAuthor.Items[0].Title)

	w = s.do(http.MethodGet, "/books?format=fb2", nil)
	var byFormat page[bookView]
	s.decode(w, &byFormat)
	s.Equal(int64(1), byFormat.Total)
	s.Equal("Dune", byFormat.Items[0].Title)
}

func (s *booksSuite) TestListBooksFTSSearch() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "The Robots of Dawn", Authors: []string{"Asimov"}})
	s.seedBook(src, bookSeed{Title: "Childhood's End", Authors: []string{"Clarke"}})

	w := s.do(http.MethodGet, "/books?q=robots", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var resp page[bookView]
	s.decode(w, &resp)
	s.Equal(int64(1), resp.Total)
	s.Equal("The Robots of Dawn", resp.Items[0].Title)
}

// TestListBooksPartialAuthorSearch confirms author= performs a token-level FTS
// search (not exact) by default.
func (s *booksSuite) TestListBooksPartialAuthorSearch() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Guards! Guards!", Authors: []string{"Terry Pratchett"}})
	s.seedBook(src, bookSeed{Title: "Dune", Authors: []string{"Frank Herbert"}})

	w := s.do(http.MethodGet, "/books?author=Pratchett", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var resp page[bookView]
	s.decode(w, &resp)
	s.Equal(int64(1), resp.Total)
	s.Equal("Guards! Guards!", resp.Items[0].Title)
}

// TestListBooksExactAuthorSearch confirms a leading '=' selects exact matching.
func (s *booksSuite) TestListBooksExactAuthorSearch() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Guards! Guards!", Authors: []string{"Terry Pratchett"}})

	exact := s.do(http.MethodGet, "/books?author==Terry%20Pratchett", nil)
	s.Require().Equal(http.StatusOK, exact.Code)
	var full page[bookView]
	s.decode(exact, &full)
	s.Equal(int64(1), full.Total)

	partial := s.do(http.MethodGet, "/books?author==Pratchett", nil)
	var none page[bookView]
	s.decode(partial, &none)
	s.Equal(int64(0), none.Total, "exact match must require the full author name")
}

func (s *booksSuite) TestGetBookDetailAndNotFound() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{
		Title: "Foundation", Series: "Foundation Saga",
		Authors: []string{"Asimov"}, Genres: []string{"SciFi"}, Annotation: "<p>Epic</p>",
	})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var bv bookView
	s.decode(w, &bv)
	s.Equal("Foundation", bv.Title)
	s.Require().NotNil(bv.Series)
	s.Equal("Foundation Saga", *bv.Series)
	s.Equal([]string{"SciFi"}, bv.Tags)
	s.Require().NotNil(bv.Annotation)
	s.Equal("<p>Epic</p>", *bv.Annotation)

	s.Equal(http.StatusNotFound, s.do(http.MethodGet, "/books/9999", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/abc", nil).Code)
}

func (s *booksSuite) TestGetBookSanitizesAnnotation() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{
		Title:      "XSS",
		Annotation: `<p>Safe <strong>text</strong></p><script>alert('xss')</script><a href="javascript:evil()">x</a>`,
	})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var bv bookView
	s.decode(w, &bv)
	s.Require().NotNil(bv.Annotation)

	// Benign formatting is preserved; the script and the javascript: URL are gone.
	s.Contains(*bv.Annotation, "<strong>text</strong>")
	s.NotContains(*bv.Annotation, "<script>")
	s.NotContains(*bv.Annotation, "javascript:")
}

func (s *booksSuite) TestGetBookExposesPublishingMetadata() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{
		Title: "Dune", Publisher: "Chilton Books", Year: 1965, Pages: 412,
		Identifiers: map[string]string{"isbn": "9780441013593", "amazon": "B00B7NPRY8"},
	})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var bv bookView
	s.decode(w, &bv)

	s.Require().NotNil(bv.Publisher)
	s.Equal("Chilton Books", *bv.Publisher)
	s.Require().NotNil(bv.Year)
	s.Equal(1965, *bv.Year)
	s.Require().NotNil(bv.Pages)
	s.Equal(412, *bv.Pages)

	urls := make(map[string]*string, len(bv.Identifiers))
	vals := make(map[string]string, len(bv.Identifiers))
	for _, id := range bv.Identifiers {
		vals[id.Type] = id.Value
		urls[id.Type] = id.URL
	}
	s.Equal("9780441013593", vals["isbn"])
	s.Equal("B00B7NPRY8", vals["amazon"])
	s.Require().NotNil(urls["isbn"])
	s.Equal("https://isbnsearch.org/isbn/9780441013593", *urls["isbn"])
	s.Require().NotNil(urls["amazon"])
	s.Equal("https://www.amazon.com/dp/B00B7NPRY8", *urls["amazon"])
}

func (s *booksSuite) TestBooksFilterByPublisher() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Publisher: "Tor"})
	s.seedBook(src, bookSeed{Title: "B", Publisher: "Gollancz"})

	w := s.do(http.MethodGet, "/books?publisher=Tor", nil)
	var resp page[bookView]
	s.decode(w, &resp)
	s.Equal(int64(1), resp.Total)
	s.Equal("A", resp.Items[0].Title)
}

func (s *booksSuite) TestGetBookBackfillsAnnotation() {
	ext := &fakeExtractor{annotation: "Recovered annotation"}
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, ext, nil, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.router = r

	src := s.seedLibrary("inpx", filepath.Join(s.dir, "lib.inpx"))
	id := s.seedBook(src, bookSeed{Title: "No Annotation", Format: "fb2"})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	var bv bookView
	s.decode(w, &bv)
	s.Require().NotNil(bv.Annotation)
	s.Equal("Recovered annotation", *bv.Annotation)
	s.Equal(1, ext.called)

	// Persisted: a second fetch needs no extraction.
	stored, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.True(stored.Annotation.Valid)
	s.Equal("Recovered annotation", stored.Annotation.String)
	s.Equal(int64(1), stored.MetadataChecked)
}

// TestGetBookCachesMissingAnnotation covers the negative cache: a book whose
// source carries no annotation is marked checked on the first view and never
// re-parsed afterwards (the slow-detail-view bug).
func (s *booksSuite) TestGetBookCachesMissingAnnotation() {
	ext := &fakeExtractor{} // empty annotation → returns (ok=false)
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, ext, nil, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.router = r

	src := s.seedLibrary("inpx", filepath.Join(s.dir, "lib.inpx"))
	id := s.seedBook(src, bookSeed{Title: "No Annotation", Format: "fb2"})

	// First view: backfill runs, finds nothing, marks the book checked.
	s.Require().Equal(http.StatusOK, s.do(http.MethodGet, "/books/"+itoa(id), nil).Code)
	s.Equal(1, ext.called)

	stored, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.False(stored.Annotation.Valid, "still no annotation")
	s.Equal(int64(1), stored.MetadataChecked, "but marked as checked")

	// Second view: a checked book must not be re-parsed.
	s.Require().Equal(http.StatusOK, s.do(http.MethodGet, "/books/"+itoa(id), nil).Code)
	s.Equal(1, ext.called, "a checked book must not be re-extracted")
}

// TestGetBookOnlineEnrichmentFillsGaps covers the online tier: a metadata-poor
// book (a PDF with no annotation) is enriched on first view — annotation, cover,
// and identifiers are persisted, content_hash is restamped so the cover
// cache-buster changes, and the book is marked enriched (queried at most once).
func (s *booksSuite) TestGetBookOnlineEnrichmentFillsGaps() {
	enr := &fakeEnricher{
		meta: ebook.Metadata{
			Annotation:  "Online description.",
			Cover:       s.jpegFixture(),
			Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
		},
		ok: true,
	}
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, nil, enr, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.router = r

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "PDF Book", Format: "pdf"}) // no annotation

	before, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)

	s.Require().Equal(http.StatusOK, s.do(http.MethodGet, "/books/"+itoa(id), nil).Code)

	after, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("Online description.", after.Annotation.String)
	s.Equal(int64(1), after.EnrichmentChecked, "marked enriched")
	s.NotEqual(before.ContentHash, after.ContentHash, "content_hash restamped for cover buster")
	s.True(s.covers.Has(id), "online cover cached")
	s.Equal(1, enr.called)

	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value)

	// Second view does not re-query (the negative/positive cache holds).
	s.Require().Equal(http.StatusOK, s.do(http.MethodGet, "/books/"+itoa(id), nil).Code)
	s.Equal(1, enr.called, "an enriched book is not re-queried")
}

// TestGetBookBackfillsIdentifiers covers the widened backfill: identifiers
// recovered from the source file (an INPX index carries none) are persisted on
// first view, and the book is marked checked.
func (s *booksSuite) TestGetBookBackfillsIdentifiers() {
	ext := &fakeExtractor{
		annotation:  "Recovered annotation",
		identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
	}
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, ext, nil, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.router = r

	src := s.seedLibrary("inpx", filepath.Join(s.dir, "lib.inpx"))
	id := s.seedBook(src, bookSeed{Title: "No IDs", Format: "fb2"})

	s.Require().Equal(http.StatusOK, s.do(http.MethodGet, "/books/"+itoa(id), nil).Code)

	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("isbn", ids[0].Type)
	s.Equal("9780441013593", ids[0].Value)

	stored, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(int64(1), stored.MetadataChecked, "book marked checked after backfill")
}

// L1: a book with an annotation but no identifiers must still get the
// file-level backfill — and the backfill must never clobber the annotation.
func (s *booksSuite) TestBackfillTriggersOnMissingIdentifiers() {
	ext := &fakeExtractor{
		annotation:  "from-file",
		identifiers: []ebook.Identifier{{Type: "isbn", Value: "9781234567897"}},
	}
	bh := NewBooks(slog.New(slog.DiscardHandler), s.db, s.guard, s.covers, ext, nil, s.covers, nil)
	r := chi.NewRouter()
	bh.Register(r)
	s.router = r

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Annotated", Annotation: "keep me"})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal(1, ext.called)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("keep me", book.Annotation.String, "backfill must not replace an existing annotation")
	ids, err := s.q.ListIdentifiersForBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("isbn", ids[0].Type)
}

func (s *booksSuite) TestDownloadFromFolder() {
	root := s.T().TempDir()
	s.Require().NoError(os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	s.Require().NoError(os.WriteFile(filepath.Join(root, "sub", "book.epub"), []byte("EPUBDATA"), 0o600))

	src := s.seedLibrary("folder", root)
	id := s.seedBook(src, bookSeed{Title: "Book", Format: "epub", SourcePath: "sub/book.epub"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/files/"+itoa(s.firstFileID(id)), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("EPUBDATA", w.Body.String())
	s.Equal("application/epub+zip", w.Header().Get("Content-Type"))
	s.Contains(w.Header().Get("Content-Disposition"), "book.epub")
}

func (s *booksSuite) TestDownloadFromINPXZip() {
	dir := s.T().TempDir()
	archive := filepath.Join(dir, "fb.zip")
	s.writeZip(archive, "book1.fb2", "FB2DATA")

	src := s.seedLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.seedBook(src, bookSeed{Title: "Zip Book", Format: "fb2", SourcePath: "fb.zip/book1.fb2"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/files/"+itoa(s.firstFileID(id)), nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("FB2DATA", w.Body.String())
	s.Equal("application/x-fictionbook+xml", w.Header().Get("Content-Type"))
}

func (s *booksSuite) TestCoverFallsBackToPlaceholder() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "No Cover"})

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
}

func (s *booksSuite) TestCoverServesCached() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Has Cover"})
	cover := s.jpegFixture()
	s.Require().NoError(s.covers.Save(id, cover))

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
	s.Equal(cover, w.Body.Bytes()) // JPEG stored as-is, served verbatim
}

func (s *booksSuite) TestServeThumbnailRoute() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Thumb Book"})
	cover := s.jpegFixture()
	s.Require().NoError(s.covers.Save(id, cover))

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover/thumbnail", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("image/jpeg", w.Header().Get("Content-Type"))
}

func (s *booksSuite) TestBooksNegativeAndEdgeCases() {
	// 1. Invalid ID formats (Bad Request)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/abc", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/0", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/-5", nil).Code)

	// 2. Non-existent book (Not Found)
	s.Equal(http.StatusNotFound, s.do(http.MethodGet, "/books/99999", nil).Code)

	// 3. Invalid file download parameters
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/abc/files/123", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/123/files/abc", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/123/files/0", nil).Code)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/123/files/-5", nil).Code)
	s.Equal(http.StatusNotFound, s.do(http.MethodGet, "/books/99999/files/99999", nil).Code)

	// 4. Mismatched book ID and file ID
	src := s.seedLibrary("folder", "/lib")
	book1ID := s.seedBook(src, bookSeed{Title: "Book 1", Format: "epub"})
	book2ID := s.seedBook(src, bookSeed{Title: "Book 2", Format: "epub"})

	file2ID := s.firstFileID(book2ID)

	// Try to download Book 2's file using Book 1's ID path
	w := s.do(http.MethodGet, "/books/"+itoa(book1ID)+"/files/"+itoa(file2ID), nil)
	s.Equal(http.StatusNotFound, w.Code, "file not found when book and file ID mismatch")

	// 5. Negative/invalid pagination query params (falls back to defaults/bounds)
	wPagination := s.do(http.MethodGet, "/books?page=-5&limit=-10", nil)
	s.Require().Equal(http.StatusOK, wPagination.Code)
	var respPagination page[bookView]
	s.decode(wPagination, &respPagination)
	s.Equal(int64(1), respPagination.Page)
	s.Equal(int64(24), respPagination.Limit)

	wPagination2 := s.do(http.MethodGet, "/books?page=abc&limit=def", nil)
	s.Require().Equal(http.StatusOK, wPagination2.Code)
	var respPagination2 page[bookView]
	s.decode(wPagination2, &respPagination2)
	s.Equal(int64(1), respPagination2.Page)
	s.Equal(int64(24), respPagination2.Limit)

	wPagination3 := s.do(http.MethodGet, "/books?limit=500", nil)
	s.Require().Equal(http.StatusOK, wPagination3.Code)
	var respPagination3 page[bookView]
	s.decode(wPagination3, &respPagination3)
	s.Equal(int64(100), respPagination3.Limit)

	// 6. Invalid book ID for cover (Bad Request)
	s.Equal(http.StatusBadRequest, s.do(http.MethodGet, "/books/abc/cover", nil).Code)
}

func (s *booksSuite) TestBookIdentifiersURLs() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{
		Title: "Identified Book",
		Identifiers: map[string]string{
			"amazon":    "B000000000",
			"goodreads": "123456",
			"google":    "abcdefg",
			"isbn":      "9780000000000",
		},
	})

	w := s.do(http.MethodGet, "/books/"+itoa(id), nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var view bookView
	s.decode(w, &view)

	s.Require().Len(view.Identifiers, 4)
	found := make(map[string]*string)
	for _, idView := range view.Identifiers {
		found[idView.Type] = idView.URL
	}

	s.Require().Contains(found, "amazon")
	s.Require().NotNil(found["amazon"])
	s.Equal("https://www.amazon.com/dp/B000000000", *found["amazon"])

	s.Require().Contains(found, "goodreads")
	s.Require().NotNil(found["goodreads"])
	s.Equal("https://www.goodreads.com/book/show/123456", *found["goodreads"])

	s.Require().Contains(found, "google")
	s.Require().NotNil(found["google"])
	s.Equal("https://books.google.com/books?id=abcdefg", *found["google"])

	s.Require().Contains(found, "isbn")
	s.Require().NotNil(found["isbn"])
	s.Equal("https://isbnsearch.org/isbn/9780000000000", *found["isbn"])
}

// TestListBooksKeepsRelationsPerBook guards the page-batched relation loading
// (P1): one IN(...) query per relation must still attribute authors, tags, and
// identifiers to their own book, never a sibling on the same page.
func (s *booksSuite) TestListBooksKeepsRelationsPerBook() {
	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{
		Title: "A", Authors: []string{"Asimov"}, Genres: []string{"SF"},
		Format: "epub", Identifiers: map[string]string{"isbn": "111"},
	})
	s.seedBook(src, bookSeed{
		Title: "B", Authors: []string{"Clarke"}, Genres: []string{"Space"},
		Format: "fb2", Identifiers: map[string]string{"isbn": "222"},
	})

	w := s.do(http.MethodGet, "/books", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var pg struct {
		Items []bookView `json:"items"`
	}
	s.decode(w, &pg)
	s.Require().Len(pg.Items, 2)
	byTitle := map[string]bookView{pg.Items[0].Title: pg.Items[0], pg.Items[1].Title: pg.Items[1]}
	s.Equal("Asimov", byTitle["A"].Authors[0].Name)
	s.Equal("Clarke", byTitle["B"].Authors[0].Name)
	s.Equal([]string{"SF"}, byTitle["A"].Tags)
	s.Equal([]string{"Space"}, byTitle["B"].Tags)
	s.Equal("111", byTitle["A"].Identifiers[0].Value)
	s.Equal("222", byTitle["B"].Identifiers[0].Value)
}

func (s *booksSuite) writeZip(path, name, content string) {
	f, err := os.Create(path)
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	entry, err := zw.Create(name)
	s.Require().NoError(err)
	_, err = entry.Write([]byte(content))
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())
}
