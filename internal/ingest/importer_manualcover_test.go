package ingest

import (
	"context"
	"os"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

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
