package ingest

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	s.Equal(Result{Added: 2}, res)

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

	// Cover cached for Alpha (has cover), absent for Beta.
	_, err = os.Stat(s.store.Path(alpha.ID))
	s.Require().NoError(err)
	_, err = os.Stat(s.store.Path(beta.ID))
	s.True(os.IsNotExist(err))

	// Second sync with no changes is a no-op (nothing re-parsed/inserted).
	res, err = parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(Result{}, res)

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
	im := newImporter(s.db, s.store)

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

	bookID, err := im.add(ctx, pdfRec, 12345)
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

	bookID2, err := im.add(ctx, epubRec, 12345)
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
	ftsTitle, ftsAuthors := s.ftsRow(bookID)
	s.Equal("Title from EPUB", ftsTitle)
	s.Equal("EPUB Author", ftsAuthors)

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

	bookID3, err := im.add(ctx, mobiRec, 12345)
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

// TestCaseFoldedDedupAcrossRecords proves M8: the same author/series/genre named
// with different casing in two separate books collapses to one row (ASCII and
// Cyrillic), and the first-seen display casing is kept.
func (s *folderSuite) TestCaseFoldedDedupAcrossRecords() {
	ctx := context.Background()
	im := newImporter(s.db, s.store)
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

	_, err := im.add(ctx, rec1, 1)
	s.Require().NoError(err)
	_, err = im.add(ctx, rec2, 2)
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
