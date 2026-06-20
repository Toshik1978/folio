package sync

import (
	"context"
	"os"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/libtype"
)

type warmSuite struct {
	baseSuite
}

// SetupTest opts this suite into cover-warming by wiring a stub extractor into
// the engine at construction (the base suite passes nil, disabling warming).
func (s *warmSuite) SetupTest() {
	s.extractor = stubExtractor{cover: s.jpegFixture(200, 100, 50)}
	s.baseSuite.SetupTest()
}

// stubExtractor returns a fixed cover for every book.
type stubExtractor struct {
	cover []byte
}

func (e stubExtractor) Cover(context.Context, int64) ([]byte, bool, error) {
	return e.cover, true, nil
}

func (s *warmSuite) seedBook(libraryID int64, title string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		LibraryID: libraryID, LibraryKey: title, Title: title,
		Language: "en", ContentHash: title, AddedAt: time.Now().UnixNano(),
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: "fb2", FileSize: 1, SourcePath: title + ".fb2",
	})
	s.Require().NoError(err)

	return id
}

func (s *warmSuite) TestINPXSyncWarmsMissingCovers() {
	s.engine.warmer.delay = 0 // no throttling in tests

	src := s.insertLibrary(libtype.INPX, "/lib/flibusta.inpx")
	b1 := s.seedBook(src.ID, "one")
	b2 := s.seedBook(src.ID, "two")
	// b2 already has a cached cover; warming must skip it.
	s.Require().NoError(s.store.Save(b2, s.jpegFixture(9, 8, 7)))

	// Give b1 a second file so the book_files id space diverges from the book id
	// space (otherwise they march in lockstep and a file/book id mix-up is
	// invisible). Warming must key covers on the book id, never a file id.
	orphanFileID, err := dbq.New(s.db).InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: b1, FileFormat: "epub", FileSize: 1, SourcePath: "one.epub",
	})
	s.Require().NoError(err)

	s.engine.Start()
	s.T().Cleanup(s.engine.Stop)

	s.Require().Eventually(func() bool {
		return s.store.Has(b1)
	}, 2*time.Second, 10*time.Millisecond)

	got, err := os.ReadFile(s.store.Path(b1))
	s.Require().NoError(err)
	s.Equal(s.jpegFixture(200, 100, 50), got) // the warmed cover (JPEG passes through unchanged)

	// b2's pre-existing cover is untouched.
	got2, err := os.ReadFile(s.store.Path(b2))
	s.Require().NoError(err)
	s.Equal(s.jpegFixture(9, 8, 7), got2)

	// Regression guard (H2): warming must iterate book ids, not book_files ids.
	// The old ListBookFilesByLibrary loop would have called warmBook with this
	// file id and cached a cover under it.
	s.False(s.store.Has(orphanFileID), "warming must use book ids, not file ids")
}

// panicExtractor panics on every Cover call — a stand-in for a parser panic the
// ebook.Parse recover doesn't cover (defense in depth for the warm goroutine).
type panicExtractor struct{}

func (panicExtractor) Cover(context.Context, int64) ([]byte, bool, error) { panic("warm boom") }

func (s *warmSuite) TestWarmSurvivesExtractorPanic() {
	s.engine.warmer.delay = 0
	s.engine.warmer.extractor = panicExtractor{}

	src := s.insertLibrary(libtype.INPX, "/lib/panic.inpx")
	s.seedBook(src.ID, "boom")

	s.NotPanics(func() { s.engine.warmer.safeWarm(src.ID) })
}

func (s *warmSuite) TestNonINPXSyncDoesNotWarm() {
	s.engine.warmer.delay = 0

	src := s.insertLibrary("stub", "/lib/folder")
	b1 := s.seedBook(src.ID, "one")

	s.engine.Start()
	s.T().Cleanup(s.engine.Stop)

	// Give the engine time to run the initial sync; no warming should occur for
	// a non-INPX library.
	time.Sleep(100 * time.Millisecond)
	s.False(s.store.Has(b1))
}
