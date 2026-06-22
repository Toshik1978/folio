package ingest

import (
	"context"
)

func (s *enrichSuite) TestBookLookupBuildsQuery() {
	ctx := context.Background()
	src := s.insertLibrary("folder", "/lib")
	bookID := s.seedBook(src.ID, "Dune", "Frank Herbert", "9780441013593")

	q, ok, err := NewBookLookup(s.db).Lookup(ctx, bookID)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("Dune", q.Title)
	s.Equal("Frank Herbert", q.Author)
	s.Equal("9780441013593", q.ISBN)
}

func (s *enrichSuite) TestBookLookupUnknownBook() {
	_, ok, err := NewBookLookup(s.db).Lookup(context.Background(), 999999)
	s.Require().NoError(err)
	s.False(ok)
}
