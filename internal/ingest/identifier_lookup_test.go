package ingest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type identifierLookupSuite struct {
	baseSuite
}

func TestIdentifierLookupSuite(t *testing.T) {
	suite.Run(t, new(identifierLookupSuite))
}

func (s *identifierLookupSuite) TestFindBookByIdentifierLowestID() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.db, s.store)
	defer im.rollback()

	// Two separate books carrying the same cleaned ISBN.
	mk := func(key, path string) bookRecord {
		return bookRecord{
			LibraryID: lib.ID, LibraryKey: key, Title: "T", Authors: []string{"A"},
			Language: "en", SourcePath: path, FileFormat: "epub", FileSize: 1,
			Identifiers: []identifier{{Type: "isbn", Value: "978-1-4668-5345-4"}},
		}
	}
	idA, err := im.add(ctx, mk("ka", "a.epub"), 1)
	s.Require().NoError(err)
	idB, err := im.add(ctx, mk("kb", "b.epub"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.Require().Less(idA, idB)

	row, err := dbq.New(s.db).FindBookByIdentifier(ctx, dbq.FindBookByIdentifierParams{
		LibraryID: lib.ID, Type: "isbn", Value: "9781466853454", // cleaned form
	})
	s.Require().NoError(err)
	s.Equal(idA, row.BookID)
	s.Equal("ka", row.LibraryKey)
}

func (s *identifierLookupSuite) TestFindBookByIdentifierMiss() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	_, err := dbq.New(s.db).FindBookByIdentifier(ctx, dbq.FindBookByIdentifierParams{
		LibraryID: lib.ID, Type: "isbn", Value: "0000000000000",
	})
	s.ErrorIs(err, sql.ErrNoRows)
}
