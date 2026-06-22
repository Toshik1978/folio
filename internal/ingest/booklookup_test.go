package ingest

import (
	"context"

	"github.com/Toshik1978/folio/internal/db/dbq"
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

// TestBookLookupPicksISBNByType verifies that Lookup selects the identifier
// typed "isbn" even when a non-isbn identifier is stored first.
func (s *enrichSuite) TestBookLookupPicksISBNByType() {
	ctx := context.Background()
	src := s.insertLibrary("folder", "/lib")
	// seedBook with empty isbn so we control identifier order manually.
	id := s.seedBook(src.ID, "Dune", "", "")
	q := dbq.New(s.db)
	// Insert a non-isbn identifier first.
	s.Require().NoError(q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
		BookID: id, Type: "asin", Value: "B000",
	}))
	// Insert the isbn identifier second.
	s.Require().NoError(q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
		BookID: id, Type: isbnType, Value: "9780441013593",
	}))

	lookup, ok, err := NewBookLookup(s.db).Lookup(ctx, id)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("9780441013593", lookup.ISBN, "Lookup selects the isbn-typed identifier, not the first one")
}
