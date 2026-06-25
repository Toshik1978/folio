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
func (f *failingCoverStore) Has(_ int64) bool             { return false }
func (f *failingCoverStore) CacheMiss(_ int64) error      { return nil }

// TestCoverWritesAreDeferredUntilCommit verifies that cover filesystem mutations
// are not applied until the import batch commits: a rollback after a queued
// cover save leaves no file on disk, while a subsequent commit does write it.
func (s *importerSuite) TestCoverWritesAreDeferredUntilCommit() {
	ctx := context.Background()
	lib := s.insertLibrary("folder", "/lib/defer")

	// Insert the book in a committed batch so we have a stable bookID.
	seed := newImporter(s.log, s.db, s.store, 1)
	base := s.rec(lib, "epub", "a.epub")
	bookID, err := seed.add(ctx, base, 1)
	s.Require().NoError(err)
	s.Require().NoError(seed.commit())
	s.False(s.store.Has(bookID), "no cover seeded yet")

	// Queue a cover save for the existing book, then roll back: nothing on disk.
	im := newImporter(s.log, s.db, s.store, 1000) // large batch; auto-commit disabled
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()
	im.saveCoverIfBetter(ctx, bookID, 0, r)
	im.rollback()
	s.False(s.store.Has(bookID), "rollback must not leave a cover on disk")

	// Queue again on a fresh importer, then commit: cover must appear.
	im2 := newImporter(s.log, s.db, s.store, 1000)
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

	// Seed a book with an EPUB edition (filePriority 4) carrying a cover.
	im := newImporter(s.log, s.db, s.store, 1)
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()
	id, err := im.add(ctx, r, 1)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	// Simulate a manual cover: overwrite the cached cover with distinguishable
	// bytes and pin cover_prio to the manual sentinel.
	s.Require().NoError(s.store.Save(id, s.coverFixtureAlt()))
	s.Require().NoError(q.UpdateBookCoverPrio(ctx, dbq.UpdateBookCoverPrioParams{CoverPrio: 1000, ID: id}))
	pinned, err := os.ReadFile(s.store.Path(id))
	s.Require().NoError(err)

	// Re-sync the same EPUB edition; a fresh importer reads the persisted prio.
	im = newImporter(s.log, s.db, s.store, 1)
	r.Cover = s.coverFixture() // a different image than the manual one
	_, err = im.add(ctx, r, 1)
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

	im := newImporter(s.log, s.db, &failingCoverStore{}, 1)
	r := s.rec(lib, "epub", "a.epub")
	r.Cover = s.coverFixture()

	id, err := im.add(ctx, r, 0)
	s.Require().NoError(err)
	s.Require().NoError(im.commit())

	book, err := q.GetBook(ctx, id)
	s.Require().NoError(err)
	s.Equal(int64(0), book.CoverPrio, "a cover that never landed must not raise cover_prio")
}
