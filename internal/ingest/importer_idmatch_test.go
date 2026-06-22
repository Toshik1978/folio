package ingest

import (
	"context"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type idMatchSuite struct {
	baseSuite
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
	im := newImporter(s.log, s.db, s.store, 1)
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
	im := newImporter(s.log, s.db, s.store, 1)
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

// Healing: two books that were split in a prior sync (different keys, shared ISBN)
// collapse to one when both files are re-seen through the reconciler.
func (s *idMatchSuite) TestHealAlreadySplitOnResync() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")

	// Prior sync state: two separate books, one file each, sharing an ISBN.
	im1 := newImporter(s.log, s.db, s.store, 1)
	a := s.recISBN(lib, "key-cixin-liu", "a.epub", "9781466853454")
	b := s.recISBN(lib, "key-liu-cixin", "b.azw3", "9781466853454")
	// Simulate the pre-fix world: force them apart by disabling pre-match on insert.
	a.DeriveIdentity, b.DeriveIdentity = false, false
	_, err := im1.add(ctx, a, 1)
	s.Require().NoError(err)
	_, err = im1.add(ctx, b, 1)
	s.Require().NoError(err)
	s.Require().NoError(im1.commit())
	s.Require().Equal(2, s.bookCount(lib))

	// Re-sync with the feature on, through the reconciler so move/prune runs.
	im2 := newImporter(s.log, s.db, s.store, 1)
	defer im2.rollback()
	rc, err := newReconciler(ctx, im2, lib.ID, 2, nopReporter{})
	s.Require().NoError(err)
	a.DeriveIdentity, b.DeriveIdentity = true, true
	s.Require().NoError(rc.upsert(ctx, a))
	s.Require().NoError(rc.upsert(ctx, b))
	_, err = rc.prune(ctx)
	s.Require().NoError(err)
	s.Require().NoError(im2.commit())

	s.Equal(1, s.bookCount(lib))
}

// A placeholder ISBN (all zeros) shared across two genuinely different books must
// not collapse them: an invalid value is never a usable grouping key.
func (s *idMatchSuite) TestPlaceholderISBNDoesNotGroup() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	_, err := im.add(ctx, s.recISBN(lib, "key-a", "a.epub", "0000000000000"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, s.recISBN(lib, "key-b", "b.epub", "0000000000000"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.bookCount(lib))
}

// An ISBN-shaped value with a bad check digit must not group: only checksum-valid
// ISBNs are trusted as strong grouping keys.
func (s *idMatchSuite) TestInvalidChecksumISBNDoesNotGroup() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	_, err := im.add(ctx, s.recISBN(lib, "key-a", "a.epub", "9781234567890"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, s.recISBN(lib, "key-b", "b.epub", "9781234567890"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.bookCount(lib))
}

// A placeholder ASIN shared across distinct books must not group either.
func (s *idMatchSuite) TestPlaceholderASINDoesNotGroup() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
	defer im.rollback()

	mk := func(key, path string) bookRecord {
		r := s.recISBN(lib, key, path, "")
		r.Identifiers = []identifier{{Type: "amazon", Value: "0000000000"}}
		return r
	}
	_, err := im.add(ctx, mk("key-a", "a.epub"), 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, mk("key-b", "b.epub"), 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.bookCount(lib))
}

func (s *idMatchSuite) TestNonWhitelistedTypeDoesNotGroup() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	im := newImporter(s.log, s.db, s.store, 1)
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
