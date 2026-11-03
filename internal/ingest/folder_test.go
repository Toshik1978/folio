package ingest

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

type folderSuite struct {
	baseSuite
}

func (s *folderSuite) TestGroupsFormatsIntoOneBook() {
	ctx := context.Background()
	dir := s.T().TempDir()
	s.writeFB2(filepath.Join(dir, "dune.fb2"), "Dune", "Frank", "Herbert", "", nil)
	s.writeFB2(filepath.Join(dir, "dune-alt.fb2"), "Dune", "Frank", "Herbert", "", nil)

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.Require().
		Equal(int64(1), s.countBooks(), "two files with the same title/author/language must be one logical book")
	books := s.booksByLibrary(src.ID)
	s.Len(s.filesFor(books["Dune"].ID), 2, "book must carry both files")
}

// TestGenresExtractedFromFolderBook proves the folder path now populates genres
// from the source file (previously folder libraries got none). FB2 <genre>
// elements are stored verbatim — raw flibusta codes, mirroring INPX.
func (s *folderSuite) TestGenresExtractedFromFolderBook() {
	ctx := context.Background()
	dir := s.T().TempDir()
	content := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Genre Book</book-title>
      <genre>sf_history</genre>
      <genre>adventure</genre>
      <author><first-name>Gene</first-name><last-name>Wolfe</last-name></author>
      <lang>en</lang>
    </title-info>
  </description>
</FictionBook>`
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "book.fb2"), []byte(content), 0o600))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	book := s.booksByLibrary(src.ID)["Genre Book"]
	genres, err := dbq.New(s.db).ListGenresForBook(ctx, book.ID)
	s.Require().NoError(err)
	names := make([]string, 0, len(genres))
	for _, g := range genres {
		names = append(names, g.Name)
	}
	s.ElementsMatch([]string{"Alternative History", "Action & Adventure"}, names)
}

func (s *folderSuite) TestSeriesGapFilledFromSiblingEdition() {
	ctx := context.Background()
	dir := s.T().TempDir()
	// a.fb2 (no series) is walked first and creates the book; b.fb2 carries the
	// series, which must be gap-filled onto the already-created logical book.
	s.writeFB2(filepath.Join(dir, "a.fb2"), "Dune", "Frank", "Herbert", "", nil)
	s.writeFB2(filepath.Join(dir, "b.fb2"), "Dune", "Frank", "Herbert", "Dune Saga", nil)

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.Require().Equal(int64(1), s.countBooks())
	book := s.booksByLibrary(src.ID)["Dune"]
	s.Require().True(book.SeriesID.Valid, "series must be gap-filled from the sibling edition")
	s.Equal("Dune Saga", s.seriesName(book.SeriesID.Int64))
}

func (s *folderSuite) writeFB2(path, title, first, last, series string, cover []byte) {
	const lang = "en"
	var seq, coverpage, binary string
	if series != "" {
		seq = fmt.Sprintf(`<sequence name=%q number="1"/>`, series)
	}
	if len(cover) > 0 {
		coverpage = `<coverpage><image href="#cover.bin"/></coverpage>`
		binary = fmt.Sprintf(`<binary id="cover.bin" content-type="image/jpeg">%s</binary>`,
			base64.StdEncoding.EncodeToString(cover))
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>%s</book-title>
      <author><first-name>%s</first-name><last-name>%s</last-name></author>
      <lang>%s</lang>
      %s
      %s
    </title-info>
  </description>
  %s
</FictionBook>`, title, first, last, lang, seq, coverpage, binary)
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

// TestSyncReportsRemovedBooks proves Removed counts files while RemovedBooks
// counts only the books left with no file.
func (s *folderSuite) TestSyncReportsRemovedBooks() {
	ctx := context.Background()
	dir := s.T().TempDir()

	s.writeFB2(filepath.Join(dir, "a1.fb2"), "Alpha", "Ann", "Author", "", nil)
	s.writeFB2(filepath.Join(dir, "a2.fb2"), "Alpha", "Ann", "Author", "", nil)
	s.writeFB2(filepath.Join(dir, "b1.fb2"), "Beta", "Bob", "Builder", "", nil)
	s.writeFB2(filepath.Join(dir, "b2.fb2"), "Beta", "Bob", "Builder", "", nil)

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Require().Len(s.booksByLibrary(src.ID), 2, "same-key files share one book")

	for _, name := range []string{"a1.fb2", "a2.fb2", "b1.fb2"} {
		s.Require().NoError(os.Remove(filepath.Join(dir, name)))
	}
	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(3, res.Removed, "files")
	s.Equal(1, res.RemovedBooks, "only Alpha lost every file")
}

func (s *folderSuite) TestSync() {
	ctx := context.Background()
	dir := s.T().TempDir()

	s.writeFB2(filepath.Join(dir, "a.fb2"), "Alpha", "Ann", "Author", "Saga", s.coverFixture())
	s.Require().NoError(os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	s.writeFB2(filepath.Join(dir, "sub", "b.fb2"), "Beta", "Bob", "Builder", "", nil)
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("nope"), 0o600))

	src := s.insertLibrary("folder", dir)

	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(2, res.Added)
	s.Zero(res.Removed)

	books := s.booksByLibrary(src.ID)
	s.Require().Len(books, 2)

	alpha := books["Alpha"]
	alphaFile := s.fileFor(alpha.ID)
	s.Equal("a.fb2", alphaFile.SourcePath)
	s.Equal("fb2", alphaFile.FileFormat)
	s.True(alpha.SeriesID.Valid, "series should be linked")
	s.Positive(alphaFile.FileSize)

	beta := books["Beta"]
	s.Equal(filepath.Join("sub", "b.fb2"), s.fileFor(beta.ID).SourcePath)
	s.False(beta.SeriesID.Valid)

	// No cover is cached at import, even for Alpha's embedded one: folder
	// covers come from the lazy cover flow.
	s.False(s.store.Has(alpha.ID))
	s.False(s.store.Has(beta.ID))

	// Second sync with no changes is a no-op (nothing re-parsed/inserted).
	res, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Zero(res.Added)
	s.Zero(res.Removed)

	// Differential: change a, add c, remove b.
	s.writeFB2(filepath.Join(dir, "a.fb2"), "Alpha Revised — now considerably longer to change the byte size",
		"Ann", "Author", "Saga", []byte("COVER-A2"))
	s.writeFB2(filepath.Join(dir, "c.fb2"), "Gamma", "Carl", "Coder", "", nil)
	s.Require().NoError(os.Remove(filepath.Join(dir, "sub", "b.fb2")))

	res, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(1, res.Added, "c (new); a is changed, not an add")
	s.Equal(1, res.Removed, "b removed")

	books = s.booksByLibrary(src.ID)
	s.Require().Len(books, 2)
	s.Contains(books, "Alpha Revised — now considerably longer to change the byte size")
	s.Contains(books, "Gamma")
	s.NotContains(books, "Beta")

	// Beta's cover is gone; the absence of Alpha's old row is also clean.
	_, err = os.Stat(s.store.Path(beta.ID))
	s.True(os.IsNotExist(err))
}

// TestAddedAtUsesFileMtime guards the "Newest" sort: a folder book must take its
// added_at from the file's mod time, not the sync-run time. Otherwise every
// folder book is stamped "now" and always outranks Calibre/INPX books (whose
// added_at comes from the source), so folder books wrongly pin to the top.
func (s *folderSuite) TestAddedAtUsesFileMtime() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "book.fb2")

	s.writeFB2(path, "Dune", "Frank", "Herbert", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old)) // pin a known mod time

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	book := s.booksByLibrary(src.ID)["Dune"]
	s.Equal(old.Unix(), book.AddedAt, "added_at must come from the file mod time, not the sync-run time")
}

// TestSameSizeEditReparsedViaMtime is the M6 regression guard: a folder file
// edited in place without changing its byte size must still be re-parsed because
// its mod time advanced. A size-only diff would skip it and keep stale metadata.
func (s *folderSuite) TestSameSizeEditReparsedViaMtime() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "book.fb2")

	s.writeFB2(path, "Dune", "Frank", "Herbert", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old)) // pin a known mod time

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Contains(s.booksByLibrary(src.ID), "Dune")

	// Overwrite with a different title of identical byte length ("Dune" -> "Mars")
	// so the file size is unchanged; advance the mod time so the diff still fires.
	s.writeFB2(path, "Mars", "Frank", "Herbert", "", nil)
	newer := old.Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, newer, newer))

	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(0, res.Added, "a same-size edit with a newer mod time is re-parsed but not an add")

	books := s.booksByLibrary(src.ID)
	s.Contains(books, "Mars")
	s.NotContains(books, "Dune")
}

// TestInPlaceEditOfPopulatedFieldPropagates proves the L4 content_hash path for
// folder libraries: editing an already-populated field of the owning edition
// (here the series) must overwrite the stored value on re-sync — not merely
// gap-fill empties. Title/author/language are held constant so it stays the same
// logical book (same group key), and the new series is the same byte length as
// the old, so the file size is unchanged — the refresh rides on content_hash
// (re-parse triggered by the advanced mod time, M6), not a size diff.
func (s *folderSuite) TestInPlaceEditOfPopulatedFieldPropagates() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "dune.fb2")

	s.writeFB2(path, "Dune", "Frank", "Herbert", "Dune Saga", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	before := s.booksByLibrary(src.ID)["Dune"]
	s.Require().True(before.SeriesID.Valid)
	s.Require().Equal("Dune Saga", s.seriesName(before.SeriesID.Int64))

	// Same title/author/language; a different, equal-length series ("Dune Saga"
	// -> "Mars Saga"), so the file size is unchanged.
	s.writeFB2(path, "Dune", "Frank", "Herbert", "Mars Saga", nil)
	newer := old.Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, newer, newer))

	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	after := s.booksByLibrary(src.ID)["Dune"]
	s.Equal(before.ID, after.ID, "same logical book, not recreated")
	s.Require().True(after.SeriesID.Valid)
	s.Equal("Mars Saga", s.seriesName(after.SeriesID.Int64),
		"editing a populated field must overwrite on re-sync, not stay stale")
}

func (s *folderSuite) TestTitleFallsBackToFilename() {
	ctx := context.Background()
	dir := s.T().TempDir()

	// FB2 with an empty title.
	s.writeFB2(filepath.Join(dir, "untitled.fb2"), "", "No", "Title", "", nil)
	src := s.insertLibrary("folder", dir)

	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	books := s.booksByLibrary(src.ID)
	s.Contains(books, "untitled")
}

func (s *folderSuite) TestMetadataFormatPriority() {
	ctx := context.Background()
	im := newImporter(s.log, s.db, s.store, 1, testRank)

	src := s.insertLibrary("folder", "/dummy")

	// 1. Add a low priority PDF book
	pdfRec := bookRecord{
		LibraryID:  src.ID,
		LibraryKey: "priority-test-key",
		Title:      "Title from PDF",
		Authors:    []string{"PDF Author"},
		Annotation: "Annotation from PDF",
		Language:   "en",
		Publisher:  "PDF Publisher",
		Year:       2020,
		FileFormat: "pdf",
		SourcePath: "book.pdf",
		FileSize:   100,
	}

	bookID, err := im.add(ctx, pdfRec, 12345, true)
	s.Require().NoError(err)

	// Verify database record has PDF metadata and metadata_format = "pdf"
	book, err := dbq.New(s.db).GetBook(ctx, bookID)
	s.Require().NoError(err)
	s.Equal("Title from PDF", book.Title)
	s.Equal("pdf", book.MetadataFormat.String)

	// 2. Add an EPUB sibling (higher priority: 4 vs 1)
	epubRec := bookRecord{
		LibraryID:  src.ID,
		LibraryKey: "priority-test-key",
		Title:      "Title from EPUB",
		Authors:    []string{"EPUB Author"},
		Annotation: "Annotation from EPUB",
		Language:   "en",
		Publisher:  "EPUB Publisher",
		Year:       2021,
		FileFormat: "epub",
		SourcePath: "book.epub",
		FileSize:   200,
	}

	bookID2, err := im.add(ctx, epubRec, 12345, true)
	s.Require().NoError(err)
	s.Equal(bookID, bookID2)

	// Verify that the logical book metadata has been updated to EPUB values
	// because epub has higher priority than pdf.
	book, err = dbq.New(s.db).GetBook(ctx, bookID)
	s.Require().NoError(err)
	s.Equal("Title from EPUB", book.Title)
	s.Equal("epub", book.MetadataFormat.String)
	s.Equal("Annotation from EPUB", book.Annotation.String)
	s.Equal("EPUB Publisher", book.Publisher.String)
	s.Equal(int64(2021), book.Year.Int64)

	// Authors are re-linked from the higher-priority edition (not left as PDF's).
	s.Equal([]string{"EPUB Author"}, s.authorsForBook(bookID))
	// The FTS row mirrors the overwritten title and authors, so search stays
	// consistent with what's displayed.
	s.True(s.ftsHas(bookID, "title", "Title from EPUB"))
	s.True(s.ftsHas(bookID, "authors", "EPUB Author"))
	s.False(s.ftsHas(bookID, "title", "Title from PDF"))

	// 3. Add a MOBI sibling (lower priority than EPUB: 2 vs 4)
	mobiRec := bookRecord{
		LibraryID:  src.ID,
		LibraryKey: "priority-test-key",
		Title:      "Title from MOBI",
		Authors:    []string{"MOBI Author"},
		Annotation: "Annotation from MOBI",
		Language:   "en",
		Publisher:  "MOBI Publisher",
		Year:       2022,
		FileFormat: "mobi",
		SourcePath: "book.mobi",
		FileSize:   300,
	}

	bookID3, err := im.add(ctx, mobiRec, 12345, true)
	s.Require().NoError(err)
	s.Equal(bookID, bookID3)

	// Verify that the logical book metadata has NOT been overwritten by MOBI
	// (it should keep the higher-priority EPUB values).
	book, err = dbq.New(s.db).GetBook(ctx, bookID)
	s.Require().NoError(err)
	s.Equal("Title from EPUB", book.Title)
	s.Equal("epub", book.MetadataFormat.String)
}

// TestImportedAtIsRunTimeNotSourceDate proves the two timestamps diverge: a folder
// book takes added_at from the (old) file mtime but imported_at from the sync-run
// time, and a re-sync never restamps imported_at.
func (s *folderSuite) TestImportedAtIsRunTimeNotSourceDate() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "book.fb2")

	s.writeFB2(path, "Dune", "Frank", "Herbert", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old)) // pin an old source date

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}

	before := time.Now().Unix()
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	after := time.Now().Unix()

	book := s.booksByLibrary(src.ID)["Dune"]
	s.Equal(old.Unix(), book.AddedAt, "added_at must be the source date (file mtime)")
	s.GreaterOrEqual(book.ImportedAt, before, "imported_at must be the sync-run time")
	s.LessOrEqual(book.ImportedAt, after, "imported_at must be the sync-run time")

	// Re-sync (no file change): imported_at must be unchanged.
	firstImported := book.ImportedAt
	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(firstImported, s.booksByLibrary(src.ID)["Dune"].ImportedAt, "re-sync must not restamp imported_at")
}

// TestResultImportedAtMatchesInsertedBooks proves Result.ImportedAt is the one
// stamp shared by every book inserted in the run, so a post-sync Deep Index can
// scope to exactly those books.
func (s *folderSuite) TestResultImportedAtMatchesInsertedBooks() {
	ctx := context.Background()
	dir := s.T().TempDir()
	s.writeFB2(filepath.Join(dir, "a.fb2"), "Dune", "Frank", "Herbert", "", nil)
	s.writeFB2(filepath.Join(dir, "b.fb2"), "Emma", "Jane", "Austen", "", nil)

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}

	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.NotZero(res.ImportedAt)
	books := s.booksByLibrary(src.ID)
	s.Len(books, 2)
	for title := range books {
		s.Equal(res.ImportedAt, books[title].ImportedAt, title)
	}
}

// TestCaseFoldedDedupAcrossRecords proves M8: the same author/series/genre named
// with different casing in two separate books collapses to one row (ASCII and
// Cyrillic), and the first-seen display casing is kept.
func (s *folderSuite) TestCaseFoldedDedupAcrossRecords() {
	ctx := context.Background()
	im := newImporter(s.log, s.db, s.store, 1, testRank)
	src := s.insertLibrary("folder", "/dummy")

	rec1 := bookRecord{
		LibraryID: src.ID, LibraryKey: "k1", Title: "Book One", Language: "en",
		Authors:    []string{"Leo Tolstoy", "Лев Толстой"},
		Genres:     []string{"Literary"},
		Series:     "War Saga",
		FileFormat: "epub", SourcePath: "1.epub", FileSize: 1,
	}
	rec2 := bookRecord{
		LibraryID: src.ID, LibraryKey: "k2", Title: "Book Two", Language: "en",
		Authors:    []string{"leo tolstoy", "лев толстой"}, // case + Cyrillic case variants
		Genres:     []string{"LITERARY"},
		Series:     "war saga",
		FileFormat: "epub", SourcePath: "2.epub", FileSize: 1,
	}

	_, err := im.add(ctx, rec1, 1, true)
	s.Require().NoError(err)
	_, err = im.add(ctx, rec2, 2, true)
	s.Require().NoError(err)

	// Two distinct authors (Latin + Cyrillic), not four; one series; one genre.
	s.Equal(2, s.countRows("authors"))
	s.Equal(1, s.countRows("series"))
	s.Equal(1, s.countRows("genres"))

	// First-writer-wins display casing is preserved.
	var seriesName string
	s.Require().NoError(s.db.QueryRowContext(ctx, "SELECT name FROM series").Scan(&seriesName))
	s.Equal("War Saga", seriesName)
}

// coverState returns the book's lazy cover-extraction marker.
func (s *folderSuite) coverState(bookID int64) int64 {
	st, err := dbq.New(s.db).GetCoverState(context.Background(), bookID)
	s.Require().NoError(err)

	return st
}

// TestImportReadsMetadataOnly proves a folder import reads each new file's
// metadata once and never its cover: covers come from the lazy flow, so the
// book is left with no cached cover and cover_state unknown (0).
func (s *folderSuite) TestImportReadsMetadataOnly() {
	ctx := context.Background()
	dir := s.T().TempDir()
	s.writeFB2(filepath.Join(dir, "a.fb2"), "Alpha", "Ann", "Author", "", s.coverFixture())

	src := s.insertLibrary("folder", dir)
	reader := &countingReader{Dispatcher: newTestDispatcher()}
	parser := FolderParser{log: s.log, parser: reader}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.Equal([]string{"a.fb2"}, reader.metadata, "the import reads the file's metadata once")
	s.Empty(reader.covers, "the import must not read covers")
	alpha := s.booksByLibrary(src.ID)["Alpha"]
	s.False(s.store.Has(alpha.ID), "no cover is cached at import")
	s.Zero(s.coverState(alpha.ID), "cover_state stays unknown for the lazy flow")
}

// lazilyCover simulates the lazy flow having extracted bookID's cover from its
// file: the cover is cached and recorded as has with the file-cover prio 1.
func (s *folderSuite) lazilyCover(bookID int64) {
	s.Require().NoError(s.store.Save(bookID, s.coverFixture()))
	s.Require().NoError(NewCoverState(s.db).MarkFileCover(context.Background(), bookID))
}

// syncTouched syncs a fresh folder library holding one FB2 at a pinned mod
// time, runs prepare on its book, then advances the file's mod time and
// re-syncs. It returns the book id.
func (s *folderSuite) syncTouched(store CoverStore, cover []byte, prepare func(bookID int64)) int64 {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "a.fb2")
	s.writeFB2(path, "Alpha", "Ann", "Author", "", cover)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, store, nopReporter{})
	s.Require().NoError(err)
	id := s.booksByLibrary(src.ID)["Alpha"].ID
	prepare(id)

	newer := old.Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, newer, newer))
	_, err = parser.Sync(ctx, src, s.db, store, nopReporter{})
	s.Require().NoError(err)

	return id
}

// TestChangedFileKeepsOnlineCover proves a cover that did not come from the
// book's file (an online-enrichment cover: has, cover_prio 0) survives the file
// changing, since nothing would fetch it again.
func (s *folderSuite) TestChangedFileKeepsOnlineCover() {
	id := s.syncTouched(s.store, nil, func(bookID int64) {
		s.Require().NoError(s.store.Save(bookID, s.coverFixture())) // has, prio 0
	})

	s.True(s.store.Has(id), "an online cover is kept")
	s.EqualValues(1, s.coverState(id), "an online cover keeps its state")
}

// TestChangedFileResetsNoneState proves a book the lazy flow found cover-less
// is re-examined after its file changes: cover_state none goes back to unknown.
func (s *folderSuite) TestChangedFileResetsNoneState() {
	id := s.syncTouched(s.store, nil, func(bookID int64) {
		s.Require().NoError(NewCoverState(s.db).Set(context.Background(), bookID, 2))
	})

	s.Zero(s.coverState(id), "a none state is reset so the new file is re-read")
}

// TestLazyCoverSavedAsFileCoverAndInvalidated drives the real lazy flow: the
// first view extracts the cover from the file and records it with cover_prio 1,
// and a later change to the file invalidates it.
func (s *folderSuite) TestLazyCoverSavedAsFileCoverAndInvalidated() {
	store, err := covers.NewStore(s.T().TempDir(),
		NewExtractor(s.db, s.log, newTestDispatcher(), db.NewWriteGuard()), NewCoverState(s.db))
	s.Require().NoError(err)

	id := s.syncTouched(store, s.coverFixture(), func(bookID int64) {
		s.Require().False(store.Has(bookID), "no cover at import")
		rr := httptest.NewRecorder()
		store.ServeCover(rr, httptest.NewRequestWithContext(context.Background(),
			http.MethodGet, "/cover", http.NoBody), bookID)
		s.Require().True(store.Has(bookID), "the first view extracts and caches the cover")
		book := s.booksByLibrary(s.libraryOf(bookID))["Alpha"]
		s.EqualValues(1, book.CoverState)
		s.EqualValues(1, book.CoverPrio, "a lazily extracted cover is recorded with prio 1")
	})

	s.False(store.Has(id), "a changed file's lazily extracted cover is deleted")
	s.Zero(s.coverState(id), "a changed file's cover_state is reset")
}

// libraryOf returns the library id of bookID.
func (s *folderSuite) libraryOf(bookID int64) int64 {
	book, err := dbq.New(s.db).GetBook(context.Background(), bookID)
	s.Require().NoError(err)

	return book.LibraryID
}

// deleteCountingStore is a CoverStore over the suite's store that counts
// Delete calls.
type deleteCountingStore struct {
	CoverStore

	deletes int
}

func (d *deleteCountingStore) Delete(bookID int64) error {
	d.deletes++

	return d.CoverStore.Delete(bookID) //nolint:wrapcheck // test pass-through
}

// TestFreshImportQueuesNoInvalidation proves importing brand-new books touches
// no cover: a new book has nothing cached to invalidate.
func (s *folderSuite) TestFreshImportQueuesNoInvalidation() {
	dir := s.T().TempDir()
	s.writeFB2(filepath.Join(dir, "a.fb2"), "Alpha", "Ann", "Author", "", nil)
	s.writeFB2(filepath.Join(dir, "b.fb2"), "Beta", "Bob", "Builder", "", nil)

	src := s.insertLibrary("folder", dir)
	store := &deleteCountingStore{CoverStore: s.store}
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(context.Background(), src, s.db, store, nopReporter{})
	s.Require().NoError(err)

	s.Len(s.booksByLibrary(src.ID), 2)
	s.Zero(store.deletes, "a fresh import queues no cover invalidation")
}

// TestChangedFileInvalidatesLazyCover proves touching a file and re-syncing
// drops the book's lazily extracted cover and resets cover_state, so the next
// view re-extracts it, while an untouched sibling book keeps its cover.
func (s *folderSuite) TestChangedFileInvalidatesLazyCover() {
	ctx := context.Background()
	dir := s.T().TempDir()
	pathA := filepath.Join(dir, "a.fb2")
	s.writeFB2(pathA, "Alpha", "Ann", "Author", "", nil)
	s.writeFB2(filepath.Join(dir, "b.fb2"), "Beta", "Bob", "Builder", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(pathA, old, old))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	books := s.booksByLibrary(src.ID)
	alpha, beta := books["Alpha"], books["Beta"]
	s.lazilyCover(alpha.ID)
	s.lazilyCover(beta.ID)

	newer := old.Add(time.Hour)
	s.Require().NoError(os.Chtimes(pathA, newer, newer))
	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.False(s.store.Has(alpha.ID), "a changed file's cached cover is deleted")
	s.Zero(s.coverState(alpha.ID), "a changed file's cover_state is reset")
	s.True(s.store.Has(beta.ID), "an unchanged book keeps its cover")
	s.EqualValues(1, s.coverState(beta.ID), "an unchanged book keeps its cover_state")
}

// TestChangedFileKeepsManualCover proves a manual cover (cover_prio 1000)
// survives its file changing: neither the cached bytes nor the state move.
func (s *folderSuite) TestChangedFileKeepsManualCover() {
	ctx := context.Background()
	dir := s.T().TempDir()
	path := filepath.Join(dir, "a.fb2")
	s.writeFB2(path, "Alpha", "Ann", "Author", "", nil)
	old := time.Unix(1_000_000, 0)
	s.Require().NoError(os.Chtimes(path, old, old))

	src := s.insertLibrary("folder", dir)
	parser := FolderParser{log: s.log, parser: newTestDispatcher()}
	_, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	alpha := s.booksByLibrary(src.ID)["Alpha"]
	s.lazilyCover(alpha.ID)
	s.Require().NoError(dbq.New(s.db).UpdateBookCoverPrio(ctx,
		dbq.UpdateBookCoverPrioParams{CoverPrio: 1000, ID: alpha.ID}))

	newer := old.Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, newer, newer))
	_, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	s.True(s.store.Has(alpha.ID), "a manual cover is never deleted")
	book := s.booksByLibrary(src.ID)["Alpha"]
	s.EqualValues(1000, book.CoverPrio, "a manual cover keeps its priority")
	s.EqualValues(1, s.coverState(alpha.ID), "a manual cover keeps its state")
}
