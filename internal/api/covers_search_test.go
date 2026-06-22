package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// stubSearcher returns canned candidates and records the query it received.
type stubSearcher struct {
	got metasearch.Query
	out []metasearch.CoverCandidate
}

func (s *stubSearcher) SearchCovers(_ context.Context, q metasearch.Query) []metasearch.CoverCandidate {
	s.got = q

	return s.out
}

type coverSearchSuite struct {
	baseSuite
}

func TestCoverSearchSuite(t *testing.T) {
	suite.Run(t, new(coverSearchSuite))
}

func (s *coverSearchSuite) newWithSearcher(srch CoverSearcher) {
	s.books = NewBooks(s.books.log, s.db, s.covers, nil, nil, s.covers, srch)
	s.rebuildRouter()
}

func (s *coverSearchSuite) TestSearchCoversSeedsQueryFromBook() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Dune", Authors: []string{"Frank Herbert"}})
	srch := &stubSearcher{out: []metasearch.CoverCandidate{
		{Source: metasearch.SourceAmazon, FullURL: "https://amz/c.jpg"},
	}}
	s.newWithSearcher(srch)

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover/search", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var got []metasearch.CoverCandidate
	s.decode(w, &got)
	s.Require().Len(got, 1)
	s.Equal("https://amz/c.jpg", got[0].FullURL)
	s.Equal("Dune", srch.got.Title)
	s.Equal("Frank Herbert", srch.got.Author)
}

func (s *coverSearchSuite) TestSearchCoversExplicitQueryOverridesSeed() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Dune"})
	srch := &stubSearcher{}
	s.newWithSearcher(srch)

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover/search?q=Foundation", nil)
	s.Require().Equal(http.StatusOK, w.Code)
	s.Equal("Foundation", srch.got.Title)
}

func (s *coverSearchSuite) TestSearchCoversDisabled() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Dune"})
	s.newWithSearcher(nil) // nil searcher disables the feature

	w := s.do(http.MethodGet, "/books/"+itoa(id)+"/cover/search", nil)
	s.Equal(http.StatusNotImplemented, w.Code)
}

func (s *coverSearchSuite) TestSearchCoversUnknownBook() {
	s.newWithSearcher(&stubSearcher{})
	w := s.do(http.MethodGet, "/books/999999/cover/search", nil)
	s.Equal(http.StatusNotFound, w.Code)
}
