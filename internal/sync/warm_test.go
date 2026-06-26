package sync

import (
	"archive/zip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	stdsync "sync"
	"time"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/ingest"
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

// noCoverExtractor parses successfully but the source carries no cover.
type noCoverExtractor struct{}

func (noCoverExtractor) Cover(context.Context, int64) ([]byte, bool, error) { return nil, false, nil }

// TestWarmNegativeCachesCoverlessBooks guards that warming pre-absorbs the
// first-view parse for books with no extractable cover: it marks cover_state
// StateNone so the grid never re-parses them. Without this the warm pass parses
// each cover-less book and discards the result, and the serve path parses it a
// second time on first view.
func (s *warmSuite) TestWarmNegativeCachesCoverlessBooks() {
	s.engine.warmer.delay = 0
	s.engine.warmer.extractor = noCoverExtractor{}

	src := s.insertLibrary(libtype.INPX, "/lib/nocover.inpx")
	id := s.seedBook(src.ID, "no-cover")

	s.engine.warmer.safeWarm(src.ID)

	state, err := dbq.New(s.db).GetCoverState(context.Background(), id)
	s.Require().NoError(err)
	s.Equal(int64(covers.StateNone), state,
		"a cover-less book must be marked StateNone by warming so the grid never re-parses it")
	s.False(s.store.Has(id),
		"warming records state, never an on-disk placeholder file")
	s.False(s.store.HasLocalCover(context.Background(), id),
		"the warmed negative mark reports no real cover")
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

// fakeBackfiller records the book ids Fill was called with.
type fakeBackfiller struct {
	mu    stdsync.Mutex
	calls []int64
}

func (f *fakeBackfiller) Fill(_ context.Context, bookID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, bookID)
	return nil
}

func (f *fakeBackfiller) called(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Contains(f.calls, id)
}

func (s *warmSuite) TestWarmBackfillsMetadata() {
	s.engine.warmer.delay = 0
	bf := &fakeBackfiller{}
	s.engine.warmer.backfiller = bf

	src := s.insertLibrary(libtype.INPX, "/lib/meta.inpx")
	id := s.seedBook(src.ID, "needs-annotation")

	s.engine.warmer.safeWarm(src.ID)

	s.True(bf.called(id), "warmer must backfill metadata for each book")
}

func (s *warmSuite) TestWarmNilBackfillerIsSafe() {
	s.engine.warmer.delay = 0
	s.engine.warmer.backfiller = nil // explicit

	src := s.insertLibrary(libtype.INPX, "/lib/nil.inpx")
	s.seedBook(src.ID, "x")

	s.NotPanics(func() { s.engine.warmer.safeWarm(src.ID) })
}

// fb2Doc builds a minimal FB2 document carrying an annotation.
func fb2Doc(title, annotation string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<FictionBook><description><title-info>
<book-title>` + title + `</book-title>
<author><first-name>A</first-name><last-name>B</last-name></author>
<annotation>` + annotation + `</annotation>
<lang>en</lang>
</title-info></description></FictionBook>`
}

func (s *warmSuite) TestINPXSyncBackfillsAnnotation() {
	s.engine.warmer.delay = 0

	// A real INPX library: an .inpx index path whose sibling archive holds the FB2.
	dir := s.T().TempDir()
	inpxPath := filepath.Join(dir, "lib.inpx")
	archivePath := filepath.Join(dir, "books.zip")
	innerName := "1.fb2"

	zf, err := os.Create(archivePath)
	s.Require().NoError(err)
	zw := zip.NewWriter(zf)
	fw, err := zw.Create(innerName)
	s.Require().NoError(err)
	_, err = fw.Write([]byte(fb2Doc("Backfilled", "Recovered over OPDS")))
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())
	s.Require().NoError(zf.Close())

	// Wire real components: the extractor parses the FB2; the backfiller persists.
	ext := ingest.NewExtractor(
		s.db,
		slog.New(slog.DiscardHandler),
		s.T().TempDir(),
		newSyncTestDispatcher(),
	) // cover-cache dir: unused in this annotation-only test
	s.engine.warmer.extractor = ext
	s.engine.warmer.backfiller = ingest.NewLocalBackfiller(slog.New(slog.DiscardHandler), s.db, db.NewWriteGuard(), ext)

	src := s.insertLibrary(libtype.INPX, inpxPath)
	id := s.seedBookWithFile(src.ID, "Backfilled", "books.zip/"+innerName)

	s.engine.warmer.safeWarm(src.ID)

	stored, err := dbq.New(s.db).GetBook(context.Background(), id)
	s.Require().NoError(err)
	s.True(stored.Annotation.Valid, "annotation backfilled from FB2")
	s.Contains(stored.Annotation.String, "Recovered over OPDS")
	s.Equal(int64(1), stored.MetadataChecked)
}

// seedBookWithFile seeds a book whose single file's SourcePath points at an INPX
// inner entry ("archive.zip/inner"). The warmer's Extractor resolves it relative
// to the library's .inpx path.
func (s *warmSuite) seedBookWithFile(libraryID int64, title, sourcePath string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		LibraryID: libraryID, LibraryKey: title, Title: title,
		Language: "en", ContentHash: title, AddedAt: time.Now().UnixNano(),
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: "fb2", FileSize: 1, SourcePath: sourcePath,
	})
	s.Require().NoError(err)

	return id
}

// newSyncTestDispatcher builds the production parser set for the sync package's
// real-extractor tests.
func newSyncTestDispatcher() *ebook.Dispatcher {
	return ebook.NewDispatcher(ebook.NewEPUB(), ebook.NewFB2(), ebook.NewMOBI(), ebook.NewPDF())
}
