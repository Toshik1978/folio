package opds

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
)

type feedsSuite struct {
	baseSuite
}

func (s *feedsSuite) TestRootNavigationFeed() {
	s.setCreds()
	w := s.getAuth("/", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "kind=navigation")

	f := s.parseFeed(w)
	s.Equal("Folio Library", f.Title)

	hrefs := make(map[string]bool)
	for i := range f.Entries {
		for _, l := range f.Entries[i].Links {
			hrefs[l.Href] = true
		}
	}
	s.True(hrefs[opdsPrefix+"/authors"], "root links to authors")
	s.True(hrefs[opdsPrefix+"/series"], "root links to series")
	s.True(hrefs[opdsPrefix+"/genres"], "root links to genres")
	s.True(hrefs[opdsPrefix+"/search"], "root links to all books")

	// Feed advertises search two ways: the spec-compliant OpenSearch
	// description link (for strict clients) and an inline templated link
	// carrying {searchTerms} (for Moon+ Reader, Librera, Stanza, which do not
	// dereference the description document).
	var hasOpenSearch, hasInline bool
	for _, l := range f.Links {
		if l.Rel != relSearch {
			continue
		}
		switch {
		case l.Href == opdsPrefix+"/opensearch.xml" && l.Type == typeOpenSearch:
			hasOpenSearch = true
		case l.Href == opdsPrefix+"/search?q={searchTerms}" && l.Type == typeSearchInline:
			hasInline = true
		}
	}
	s.True(hasOpenSearch, "feed advertises OpenSearch description")
	s.True(hasInline, "feed advertises inline templated search link")
}

func (s *feedsSuite) TestAuthorsFeed() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Authors: []string{"Asimov"}})
	s.seedBook(src, bookSeed{Title: "Dune", Authors: []string{"Herbert"}})

	w := s.getAuth("/authors", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	f := s.parseFeed(w)
	s.Len(f.Entries, 2)

	// Each author entry links to a filtered search feed.
	var asimov *entry
	for i := range f.Entries {
		if f.Entries[i].Title == "Asimov" {
			asimov = &f.Entries[i]
		}
	}
	s.Require().NotNil(asimov)
	s.Contains(asimov.Links[0].Href, "/opds/search?author=Asimov")
}

func (s *feedsSuite) TestGenresFeed() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Genres: []string{"SciFi"}})
	s.seedBook(src, bookSeed{Title: "Dune", Genres: []string{"SciFi"}})
	s.seedBook(src, bookSeed{Title: "Hamlet", Genres: []string{"Drama"}})

	w := s.getAuth("/genres", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	f := s.parseFeed(w)
	s.Len(f.Entries, 2)

	var sciFi *entry
	for i := range f.Entries {
		if f.Entries[i].Title == "SciFi" {
			sciFi = &f.Entries[i]
		}
	}
	s.Require().NotNil(sciFi)
	// Each tag entry links to a tag-filtered acquisition feed.
	s.Contains(sciFi.Links[0].Href, "/opds/search?tag=SciFi")
}

func (s *feedsSuite) TestSearchFiltersByTag() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Genres: []string{"SciFi"}})
	s.seedBook(src, bookSeed{Title: "Hamlet", Genres: []string{"Drama"}})

	f := s.parseFeed(s.getAuth("/search?tag=SciFi", testUser, testPass))
	s.Require().Len(f.Entries, 1)
	s.Equal("Foundation", f.Entries[0].Title)
}

func (s *feedsSuite) TestSearchAcquisitionFeed() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{
		Title: "Foundation", Format: "epub", Authors: []string{"Asimov"},
		Annotation: "<p>Galactic empire</p>", Publisher: "Gnome Press", Year: 1951,
	})

	w := s.getAuth("/search", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "kind=acquisition")

	f := s.parseFeed(w)
	s.Require().Len(f.Entries, 1)
	e := f.Entries[0]
	s.Equal("Foundation", e.Title)
	s.Require().Len(e.Authors, 1)
	s.Equal("Asimov", e.Authors[0].Name)

	// dc:-prefixed elements are emitted with xmlns:dc on the root (valid OPDS),
	// but Go's xml.Unmarshal doesn't remap prefixed names to fields — assert on
	// the wire output instead.
	body := w.Body.String()
	s.Contains(body, "<dc:publisher>Gnome Press</dc:publisher>")
	s.Contains(body, "<dc:issued>1951</dc:issued>")

	rels := make(map[string]string)
	for _, l := range e.Links {
		rels[l.Rel] = l.Href
	}
	s.Equal(opdsPrefix+"/books/1/files/1", rels[relAcquisition])
	s.Equal(opdsPrefix+"/books/1/cover?v=Foundation-0", rels[relImage])
	s.Equal("application/epub+zip", linkType(e.Links, relAcquisition))
}

func (s *feedsSuite) TestSearchFiltersByAuthorAndQuery() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Authors: []string{"Asimov"}})
	s.seedBook(src, bookSeed{Title: "Childhoods End", Authors: []string{"Clarke"}})

	byAuthor := s.parseFeed(s.getAuth("/search?author=Asimov", testUser, testPass))
	s.Require().Len(byAuthor.Entries, 1)
	s.Equal("Foundation", byAuthor.Entries[0].Title)

	byQuery := s.parseFeed(s.getAuth("/search?q=childhoods", testUser, testPass))
	s.Require().Len(byQuery.Entries, 1)
	s.Equal("Childhoods End", byQuery.Entries[0].Title)
}

// TestSearchTrimsWhitespaceQuery verifies a whitespace-only q is treated as
// empty (listing every book) rather than run as an FTS query, matching the REST
// list handler.
func (s *feedsSuite) TestSearchTrimsWhitespaceQuery() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Authors: []string{"Asimov"}})
	s.seedBook(src, bookSeed{Title: "Childhoods End", Authors: []string{"Clarke"}})

	f := s.parseFeed(s.getAuth("/search?q=%20%20", testUser, testPass))
	s.Len(f.Entries, 2, "whitespace-only q must list all books, not run an FTS query")
}

func (s *feedsSuite) TestSearchAnnotationSanitized() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{
		Title:      "Foundation",
		Annotation: `<p>Galactic empire</p><script>alert(1)</script>`,
	})

	f := s.parseFeed(s.getAuth("/search", testUser, testPass))
	s.Require().Len(f.Entries, 1)
	s.Require().NotNil(f.Entries[0].Content)

	got := f.Entries[0].Content.Value
	s.Contains(got, "Galactic empire")
	s.NotContains(got, "<script>")
	s.NotContains(got, "alert(1)")
}

func (s *feedsSuite) TestSearchPagination() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	for i := range defaultLimit + 5 {
		s.seedBook(src, bookSeed{Title: fmt.Sprintf("Book %03d", i)})
	}

	// Page 1: full page, advertises next but not previous.
	first := s.parseFeed(s.getAuth("/search", testUser, testPass))
	s.Len(first.Entries, defaultLimit)
	s.Equal(opdsPrefix+"/search?page=2", linkHref(first.Links, relNext))
	s.Empty(linkHref(first.Links, relPrevious))

	// Page 2: remaining 5, advertises previous but not next.
	second := s.parseFeed(s.getAuth("/search?page=2", testUser, testPass))
	s.Len(second.Entries, 5)
	s.Equal(opdsPrefix+"/search?page=1", linkHref(second.Links, relPrevious))
	s.Empty(linkHref(second.Links, relNext))
}

func (s *feedsSuite) TestSearchPaginationPreservesFilters() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	for i := range defaultLimit + 1 {
		s.seedBook(src, bookSeed{Title: fmt.Sprintf("Book %03d", i), Authors: []string{"Asimov"}})
	}

	first := s.parseFeed(s.getAuth("/search?author=Asimov", testUser, testPass))
	s.Len(first.Entries, defaultLimit)
	next := linkHref(first.Links, relNext)
	s.Contains(next, "author=Asimov")
	s.Contains(next, "page=2")
}

func (s *feedsSuite) TestAuthorsPagination() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	for i := range defaultLimit + 3 {
		s.seedBook(src, bookSeed{
			Title:   fmt.Sprintf("Book %03d", i),
			Authors: []string{fmt.Sprintf("Author %03d", i)},
		})
	}

	first := s.parseFeed(s.getAuth("/authors", testUser, testPass))
	s.Len(first.Entries, defaultLimit)
	s.Equal(opdsPrefix+"/authors?page=2", linkHref(first.Links, relNext))
	s.Empty(linkHref(first.Links, relPrevious))

	second := s.parseFeed(s.getAuth("/authors?page=2", testUser, testPass))
	s.Len(second.Entries, 3)
	s.Equal(opdsPrefix+"/authors?page=1", linkHref(second.Links, relPrevious))
	s.Empty(linkHref(second.Links, relNext))
}

func (s *feedsSuite) TestSeriesPagination() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	for i := range defaultLimit + 2 {
		s.seedBook(src, bookSeed{
			Title:  fmt.Sprintf("Book %03d", i),
			Series: fmt.Sprintf("Series %03d", i),
		})
	}

	first := s.parseFeed(s.getAuth("/series", testUser, testPass))
	s.Len(first.Entries, defaultLimit)
	s.Equal(opdsPrefix+"/series?page=2", linkHref(first.Links, relNext))

	second := s.parseFeed(s.getAuth("/series?page=2", testUser, testPass))
	s.Len(second.Entries, 2)
	s.Equal(opdsPrefix+"/series?page=1", linkHref(second.Links, relPrevious))
}

func (s *feedsSuite) TestGenresPagination() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	for i := range defaultLimit + 2 {
		s.seedBook(src, bookSeed{
			Title:  fmt.Sprintf("Book %03d", i),
			Genres: []string{fmt.Sprintf("Tag %03d", i)},
		})
	}

	first := s.parseFeed(s.getAuth("/genres", testUser, testPass))
	s.Len(first.Entries, defaultLimit)
	s.Equal(opdsPrefix+"/genres?page=2", linkHref(first.Links, relNext))

	second := s.parseFeed(s.getAuth("/genres?page=2", testUser, testPass))
	s.Len(second.Entries, 2)
	s.Equal(opdsPrefix+"/genres?page=1", linkHref(second.Links, relPrevious))
}

func (s *feedsSuite) TestOpenSearchDescription() {
	s.setCreds()
	w := s.getAuth("/opensearch.xml", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal(typeOpenSearch, w.Header().Get("Content-Type"))
	// The Url template must be absolute (scheme + host from the request) so
	// readers like Koodo can fetch it — r.URL.Host is empty on inbound requests,
	// the host comes from r.Host. httptest.NewRequest defaults r.Host to
	// "example.com".
	s.Contains(w.Body.String(), `template="http://example.com/opds/search?q={searchTerms}"`)
	s.NotContains(w.Body.String(), "http:///opds")
}

// TestOpenSearchPublicURL verifies that a configured PUBLIC_URL is authoritative
// for the advertised template and that a forged X-Forwarded-Host cannot poison
// it (L8).
func (s *feedsSuite) TestOpenSearchPublicURL() {
	s.setCreds()
	router := chi.NewRouter()
	New(slog.New(slog.DiscardHandler), s.db, s.covers, nil, s.authn, "https://folio.example.com/").Register(router)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/opensearch.xml", http.NoBody)
	r.Header.Set("X-Forwarded-Host", "evil.example.org")
	r.SetBasicAuth(testUser, testPass)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `template="https://folio.example.com/opds/search?q={searchTerms}"`)
	s.NotContains(w.Body.String(), "evil.example.org")
}

func (s *feedsSuite) TestPluralUsesExplicitForms() {
	s.Equal("1 book", plural(1, "book", "books"))
	s.Equal("2 books", plural(2, "book", "books"))
	s.Equal("0 books", plural(0, "book", "books"))
}

func (s *feedsSuite) TestPageParamClampsHugeValues() {
	r := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/opds/search?page=9223372036854775807", http.NoBody,
	)
	s.LessOrEqual(pageParam(r), int64(maxPage)) // shares the REST cap
}

func (s *feedsSuite) TestBookEntryThumbnailUsesThumbnailRoute() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "Foundation", Format: "epub"})

	w := s.getAuth("/search", testUser, testPass)
	s.Require().Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `/cover/thumbnail?v=`, "thumbnail rel points at the thumbnail route")
	s.Regexp(`rel="http://opds-spec.org/image/thumbnail"[^>]*href="[^"]*/cover/thumbnail\?v=`, body)
	s.Regexp(`rel="http://opds-spec.org/image"[^>]*href="[^"]*/cover\?v=`, body)
	s.Contains(body, `/cover/thumbnail?v=`)
	s.Regexp(`/cover/thumbnail\?v=[^"]*-t400q85`, body, "thumbnail href carries the cache-spec token")
}

func linkType(links []link, rel string) string {
	for _, l := range links {
		if l.Rel == rel {
			return l.Type
		}
	}

	return ""
}

func linkHref(links []link, rel string) string {
	for _, l := range links {
		if l.Rel == rel {
			return l.Href
		}
	}

	return ""
}
