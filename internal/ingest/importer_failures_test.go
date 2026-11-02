package ingest

import (
	"context"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// seedReadFailures records a metadata and a cover failure for bookID.
func (s *importerSuite) seedReadFailures(ctx context.Context, bookID int64) {
	q := dbq.New(s.db)
	for _, flow := range []string{"metadata", "cover"} {
		s.Require().NoError(q.UpsertReadFailure(ctx, dbq.UpsertReadFailureParams{
			BookID: bookID, Flow: flow, Reason: "corrupt", Fingerprint: "fp", ReaderVersion: 1, RecordedAt: 1,
		}))
	}
}

// countReadFailures returns how many failure rows bookID has.
func (s *importerSuite) countReadFailures(bookID int64) int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM book_read_failures WHERE book_id = ?`, bookID).Scan(&n))

	return n
}

// TestChangedFileDropsReadFailures proves a changed file of an existing book
// loses both its metadata and cover failure records.
func (s *importerSuite) TestChangedFileDropsReadFailures() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/failures-changed")

	im := newImporter(s.log, s.db, s.store, 1, testRank)
	id, err := im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.seedReadFailures(ctx, id)

	_, err = im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Zero(s.countReadFailures(id), "a changed file drops both failure records")
}

// TestUnchangedResyncKeepsReadFailures proves an unchanged re-sync leaves the
// failure records alone.
func (s *importerSuite) TestUnchangedResyncKeepsReadFailures() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/failures-unchanged")

	im := newImporter(s.log, s.db, s.store, 1, testRank)
	id, err := im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.seedReadFailures(ctx, id)

	_, err = im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, false)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.Equal(2, s.countReadFailures(id), "an unchanged re-sync keeps the failure records")
}

// TestRegroupedFileResetsBothBooks proves a file that moves to another existing
// book with unchanged bytes (an INPX index fix changed its grouping key) is
// treated as changed on both sides: the book it joined loses its "none" cover
// marker and read failures, and the book it left loses the file-derived cover
// and read failures that may describe it.
func (s *importerSuite) TestRegroupedFileResetsBothBooks() {
	ctx := context.Background()
	lib := s.insertLibrary("inpx", "/lib/regroup.inpx")
	rec := func(key, format, path string) bookRecord {
		r := s.rec(lib, format, path)
		r.LibraryKey, r.Title, r.Mtime = key, key, 7

		return r
	}
	sync := func(recs ...bookRecord) {
		im := newImporter(s.log, s.db, s.store, 1, testRank)
		rc, err := newReconciler(ctx, im, lib.ID, 1, nopReporter{})
		s.Require().NoError(err)
		for i := range recs {
			s.Require().NoError(rc.upsert(ctx, recs[i]))
		}
		s.Require().NoError(im.commit())
	}
	moving := rec("left", "epub", "moving.epub")

	sync(rec("joined", "fb2", "joined.fb2"), rec("left", "fb2", "left.fb2"), moving)
	books := s.booksByLibrary(lib.ID)
	joinedID, leftID := books["joined"].ID, books["left"].ID
	cs := NewCoverState(s.db)
	s.Require().NoError(cs.Set(ctx, joinedID, int8(coverStateNone)))
	s.Require().NoError(s.store.Save(leftID, s.coverFixture()))
	s.Require().NoError(cs.MarkFileCover(ctx, leftID))
	s.seedReadFailures(ctx, joinedID)
	s.seedReadFailures(ctx, leftID)

	moving.LibraryKey = "joined" // same bytes, new grouping key
	sync(rec("joined", "fb2", "joined.fb2"), rec("left", "fb2", "left.fb2"), moving)

	state, err := cs.Get(ctx, joinedID)
	s.Require().NoError(err)
	s.Zero(state, "the book the file joined loses its none marker")
	s.Zero(s.countReadFailures(joinedID))
	state, err = cs.Get(ctx, leftID)
	s.Require().NoError(err)
	s.Zero(state, "the book the file left loses its file-derived cover")
	s.False(s.store.Has(leftID))
	s.Zero(s.countReadFailures(leftID))
}
