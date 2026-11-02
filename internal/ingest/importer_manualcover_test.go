package ingest

import (
	"context"
	"errors"
	"os"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// failingCoverStore is a CoverStore stub whose Save always returns an error.
// Delete and Has are no-ops so the rest of the import pipeline can run cleanly.
type failingCoverStore struct{}

func (f *failingCoverStore) Save(_ int64, _ []byte) error { return errors.New("disk full") }
func (f *failingCoverStore) Delete(_ int64) error         { return nil }

// TestCoverWritesAreDeferredUntilCommit verifies that cover filesystem mutations
// are not applied until the import batch commits: a rollback after a queued
// cover save leaves no file on disk, while a subsequent commit does write it.
func (s *importerSuite) TestCoverWritesAreDeferredUntilCommit() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/defer")

	// Insert the book in a committed batch so we have a stable bookID.
	seed := newImporter(s.log, s.db, s.store, 1, testRank)
	base := s.rec(lib, "epub", "a.epub")
	bookID, err := seed.add(ctx, base, 1, true)
	s.Require().NoError(err)
	s.Require().NoError(seed.commit())
	s.False(s.store.Has(bookID), "no cover seeded yet")

	// Queue a cover save for the existing book, then roll back: nothing on disk.
	im := newImporter(s.log, s.db, s.store, 1000, testRank) // large batch; auto-commit disabled
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()
	im.saveCoverIfBetter(ctx, bookID, 0, r)
	im.rollback()
	s.False(s.store.Has(bookID), "rollback must not leave a cover on disk")

	// Queue again on a fresh importer, then commit: cover must appear.
	im2 := newImporter(s.log, s.db, s.store, 1000, testRank)
	im2.saveCoverIfBetter(ctx, bookID, 0, r)
	s.Require().NoError(im2.commit())
	s.True(s.store.Has(bookID), "commit must flush the deferred cover to disk")
}

// TestManualCoverSurvivesResync proves a cover pinned at the manual sentinel
// priority (1000, matching api.manualCoverPrio) is never downgraded by a later
// import, whatever the incoming file's format priority. This is the end-to-end
// guarantee behind a user-set cover.
func (s *importerSuite) TestManualCoverSurvivesResync() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib")
	q := dbq.New(s.db)

	// Seed a book with an EPUB edition (rank 4) carrying a cover.
	im := newImporter(s.log, s.db, s.store, 1, testRank)
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()
	id, err := im.add(ctx, r, 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	// Simulate a manual cover: overwrite the cached cover with distinguishable
	// bytes and pin cover_prio to the manual sentinel.
	s.Require().NoError(s.store.Save(id, s.coverFixtureAlt()))
	s.Require().NoError(q.UpdateBookCoverPrio(ctx, dbq.UpdateBookCoverPrioParams{CoverPrio: 1000, ID: id}))
	pinned, err := os.ReadFile(s.store.Path(id))
	s.Require().NoError(err)

	// Re-sync the same EPUB edition; a fresh importer reads the persisted prio.
	im = newImporter(s.log, s.db, s.store, 1, testRank)
	r.Cover = s.coverFixture() // a different image than the manual one
	_, err = im.add(ctx, r, 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	// The manual cover bytes and the pinned priority are both intact.
	after, err := os.ReadFile(s.store.Path(id))
	s.Require().NoError(err)
	s.Equal(pinned, after, "sync must not overwrite a manual cover")
	book, err := q.GetBook(ctx, id)
	s.Require().NoError(err)
	s.EqualValues(1000, book.CoverPrio)
}

// TestCoverPrioNotRaisedWhenSaveFails guards that cover_prio in the DB is only
// bumped after the cover file actually lands on disk. If Save returns an error
// the priority must stay at 0 so a later, lower-priority edition can still win.
func (s *importerSuite) TestCoverPrioNotRaisedWhenSaveFails() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/failing-cover")
	q := dbq.New(s.db)

	im := newImporter(s.log, s.db, &failingCoverStore{}, 1, testRank)
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()

	id, err := im.add(ctx, r, 0, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := q.GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal(int64(0), book.CoverPrio, "a cover that never landed must not raise cover_prio")
}

// TestCoverBytesSavedDespiteCoverlessSibling proves cover bytes a record brings
// (Calibre's cover.jpg, read once per book) are saved even when a sibling
// format of the same book, changed in the same run, carries none: the
// coverless record must not invalidate a cover saved this run.
func (s *importerSuite) TestCoverBytesSavedDespiteCoverlessSibling() {
	ctx := context.Background()
	lib := s.insertLibrary("calibre", "/lib/calibre")

	im := newImporter(s.log, s.db, s.store, 1000, testRank)
	withCover := s.rec(lib, "epub", "a.epub")
	withCover.Cover = s.coverFixture()
	id, err := im.add(ctx, withCover, 1, true)
	s.Require().NoError(err)
	_, err = im.add(ctx, s.rec(lib, "mobi", "a.mobi"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.True(s.store.Has(id), "the record's cover bytes are saved")
	book, err := dbq.New(s.db).GetBook(ctx, id)
	s.Require().NoError(err)
	s.EqualValues(testRank("epub"), book.CoverPrio)
}

// TestRolledBackInvalidationKeepsCover proves a cover invalidation queued in a
// batch that rolls back never runs: the cached cover and its state survive.
func (s *importerSuite) TestRolledBackInvalidationKeepsCover() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/rollback")
	q := dbq.New(s.db)

	seed := newImporter(s.log, s.db, s.store, 1, testRank)
	id, err := seed.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(seed.commit())
	s.Require().NoError(s.store.Save(id, s.coverFixture()))
	s.Require().NoError(NewCoverState(s.db).MarkFileCover(ctx, id)) // a lazily extracted file cover

	im := newImporter(s.log, s.db, s.store, 1000, testRank)
	_, err = im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	im.rollback()

	s.True(s.store.Has(id), "a rolled-back batch leaves the cover on disk")
	st, err := q.GetCoverState(ctx, id)
	s.Require().NoError(err)
	s.EqualValues(1, st, "a rolled-back batch leaves cover_state untouched")

	// The same change committed does invalidate it.
	im = newImporter(s.log, s.db, s.store, 1000, testRank)
	_, err = im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.False(s.store.Has(id), "a committed change deletes the cover")
	st, err = q.GetCoverState(ctx, id)
	s.Require().NoError(err)
	s.Zero(st, "a committed change resets cover_state")
}

// TestUnchangedRecordKeepsCover proves a record the reconciler reports as
// unchanged never invalidates the book's cover.
func (s *importerSuite) TestUnchangedRecordKeepsCover() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/unchanged")

	im := newImporter(s.log, s.db, s.store, 1, testRank)
	id, err := im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.Require().NoError(s.store.Save(id, s.coverFixture()))

	_, err = im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, false)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.True(s.store.Has(id), "an unchanged record leaves the cover alone")
}

// TestNewSiblingFileInvalidatesFileCover proves a new file grouped onto an
// existing book invalidates the book's file-derived cover: only a brand-new
// book skips invalidation.
func (s *importerSuite) TestNewSiblingFileInvalidatesFileCover() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/sibling")

	im := newImporter(s.log, s.db, s.store, 1, testRank)
	id, err := im.add(ctx, s.rec(lib, "epub", "a.epub"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())
	s.Require().NoError(s.store.Save(id, s.coverFixture()))
	s.Require().NoError(NewCoverState(s.db).MarkFileCover(ctx, id))

	_, err = im.add(ctx, s.rec(lib, "pdf", "a.pdf"), 1, true)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	s.False(s.store.Has(id), "a new sibling file invalidates the file cover")
}
