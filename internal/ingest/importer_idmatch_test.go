package ingest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type idMatchSuite struct {
	baseSuite
}

func TestIDMatchSuite(t *testing.T) {
	suite.Run(t, new(idMatchSuite))
}

// rec with a shared ISBN but a different LibraryKey (simulating author-order drift).
func (s *idMatchSuite) recISBN(lib dbq.Library, key, path, isbn string) bookRecord {
	return bookRecord{
		LibraryID: lib.ID, LibraryKey: key, Title: "T", Authors: []string{"A"},
		Language: "en", SourcePath: path, FileFormat: "epub", FileSize: 1,
		DeriveIdentity: true,
		Identifiers:    []identifier{{Type: "isbn", Value: isbn}},
	}
}

func (s *idMatchSuite) bookCount(lib dbq.Library) int {
	ctx := context.Background()
	books, err := dbq.New(s.db).ListBooks(ctx, dbq.ListBooksParams{Limit: 1000, Offset: 0})
	s.Require().NoError(err)
	n := 0
	for i := range books {
		if books[i].LibraryID == lib.ID {
			n++
		}
	}

	return n
}

func (s *idMatchSuite) TestSharedISBNGroupsDespiteDifferentKey() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.db, s.store)
	defer im.rollback()

	id1, err := im.add(ctx, s.recISBN(lib, "key-cixin-liu", "a.epub", "9781466853454"), 1)
	s.Require().NoError(err)
	// Different library_key (author order drift), same ISBN -> must group onto id1.
	id2, err := im.add(ctx, s.recISBN(lib, "key-liu-cixin", "b.azw3", "978-1-4668-5345-4"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(id1, id2)
	s.Equal(1, s.bookCount(lib))

	files, err := dbq.New(s.db).ListFilesForBook(ctx, id1)
	s.Require().NoError(err)
	s.Len(files, 2)
}

func (s *idMatchSuite) TestCalibreNotIdentifierMatched() {
	ctx := context.Background()
	lib := s.insertLibrary("calibre", "/lib")
	im := newImporter(s.db, s.store)
	defer im.rollback()

	// Two distinct Calibre books sharing an ISBN: DeriveIdentity is false -> stay split.
	a := s.recISBN(lib, "calibre:1", "a.epub", "9781466853454")
	a.DeriveIdentity = false
	b := s.recISBN(lib, "calibre:2", "b.epub", "9781466853454")
	b.DeriveIdentity = false
	_, err := im.add(ctx, a, 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, b, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.bookCount(lib))
}

func (s *idMatchSuite) TestNonWhitelistedTypeDoesNotGroup() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.db, s.store)
	defer im.rollback()

	mk := func(key, path string) bookRecord {
		r := s.recISBN(lib, key, path, "")
		r.Identifiers = []identifier{{Type: "barnesnoble", Value: "XYZ123"}}
		return r
	}
	_, err := im.add(ctx, mk("k1", "a.epub"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, mk("k2", "b.epub"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.bookCount(lib)) // different keys, non-strong id -> two books
}
