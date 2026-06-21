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
