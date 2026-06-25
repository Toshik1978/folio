package openlibrary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// TestOpenLibrary is the package's single entry point; every suite is registered here.
func TestOpenLibrary(t *testing.T) {
	suite.Run(t, new(openLibrarySuite))
}

type openLibrarySuite struct {
	suite.Suite
}

func (s *openLibrarySuite) TestSearchCovers() {
	// Capture the request the source issues so we can assert on it after the call
	// completes, rather than failing from inside the server goroutine.
	var gotPath, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.URL.Query().Get("title")
		_, _ = w.Write([]byte(`{"docs":[
			{"title":"Dune","cover_i":123},
			{"title":"No Cover"}
		]}`))
	}))
	defer srv.Close()

	src := New(5 * time.Second)
	src.baseURL = srv.URL

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Equal("/search.json", gotPath)
	s.Equal("Dune", gotTitle)
	s.Require().Len(got, 1, "only docs with cover_i yield candidates")
	s.Equal(metasearch.SourceOpenLibrary, got[0].Source)
	s.Equal("https://covers.openlibrary.org/b/id/123-L.jpg", got[0].FullURL)
	s.Equal("https://covers.openlibrary.org/b/id/123-M.jpg", got[0].ThumbURL)
}

func (s *openLibrarySuite) TestSearchByISBNIgnoresTitle() {
	// With an ISBN present, the query must be ISBN-only: a stored series-subtitle
	// title would otherwise zero the result for the exact edition.
	var gotISBN, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotISBN = r.URL.Query().Get("isbn")
		gotTitle = r.URL.Query().Get("title")
		_, _ = w.Write([]byte(`{"docs":[{"title":"Death's End","cover_i":7893958}]}`))
	}))
	defer srv.Close()

	src := New(5 * time.Second)
	src.baseURL = srv.URL

	got, err := src.SearchCovers(context.Background(), metasearch.Query{
		Title: "Death's End (Remembrance of Earth's Past)",
		ISBN:  "9781466853454",
	})
	s.Require().NoError(err)
	s.Equal("9781466853454", gotISBN)
	s.Empty(gotTitle, "title must not be sent alongside an exact ISBN")
	s.Require().Len(got, 1)
	s.Equal("https://covers.openlibrary.org/b/id/7893958-L.jpg", got[0].FullURL)
}

func (s *openLibrarySuite) TestCapabilities() {
	src := New(time.Second)
	s.Equal(metasearch.SourceOpenLibrary, src.Name())
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}
