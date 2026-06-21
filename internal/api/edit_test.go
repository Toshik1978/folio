package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

type editSuite struct {
	baseSuite
}

func TestEditSuite(t *testing.T) {
	suite.Run(t, new(editSuite))
}

// rawPut issues a PUT with a raw (non-JSON) body, used for cover byte uploads.
func (s *editSuite) rawPut(path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

func (s *editSuite) TestUploadCoverPinsPriority() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.rawPut("/books/"+itoa(id)+"/cover", s.jpegFixture())
	s.Require().Equal(http.StatusOK, w.Code)

	// A cover file now exists for the book.
	s.True(s.covers.Has(id), "uploaded cover is cached on disk")

	// cover_prio is pinned to the manual sentinel so no sync downgrades it.
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(manualCoverPrio, book.CoverPrio)
}

func (s *editSuite) TestUploadCoverRejectsNonImage() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.rawPut("/books/"+itoa(id)+"/cover", []byte("not an image"))
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUploadCoverUnknownBook() {
	w := s.rawPut("/books/999999/cover", s.jpegFixture())
	s.Equal(http.StatusNotFound, w.Code)
}

func (s *editSuite) TestSetCoverFromURL() {
	// A local HTTP server stands in for the remote image host.
	img := s.jpegFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": srv.URL})
	s.Require().Equal(http.StatusOK, w.Code)
	s.True(s.covers.Has(id), "fetched cover is cached")

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(manualCoverPrio, book.CoverPrio)
}

func (s *editSuite) TestSetCoverFromURLRejectsBadScheme() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": "file:///etc/passwd"})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestSetCoverFromURLMissingURL() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUpdateBookOverwritesAndLocks() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Old", Authors: []string{"Nobody"}, Genres: []string{"History"}})

	body := map[string]any{
		"title":      "Dune",
		"authors":    []string{"Frank Herbert"},
		"genres":     []string{"Science Fiction"},
		"series":     "Dune Chronicles",
		"year":       1965,
		"publisher":  "Ace",
		"annotation": "Desert planet.",
	}
	w := s.do(http.MethodPut, "/books/"+itoa(id), body)
	s.Require().Equal(http.StatusOK, w.Code)

	var got bookView
	s.decode(w, &got)
	s.Equal("Dune", got.Title)
	s.Require().Len(got.Authors, 1)
	s.Equal("Frank Herbert", got.Authors[0].Name)
	s.Equal([]string{"Science Fiction"}, got.Tags)
	s.Require().NotNil(got.Year)
	s.Equal(1965, *got.Year)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.ManuallyMatched, "a manual edit locks the book against sync revert")
}

func (s *editSuite) TestUpdateBookRequiresTitle() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Old"})

	w := s.do(http.MethodPut, "/books/"+itoa(id), map[string]any{"title": "  "})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUpdateBookUnknown() {
	w := s.do(http.MethodPut, "/books/999999", map[string]any{"title": "x"})
	s.Equal(http.StatusNotFound, w.Code)
}
