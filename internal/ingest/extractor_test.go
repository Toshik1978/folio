package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

type extractorSuite struct {
	baseSuite

	reader *countingReader
}

// countingReader is a bookReader over the standard Dispatcher that records the
// name of every Source it reads.
type countingReader struct {
	*ebook.Dispatcher

	mu       sync.Mutex
	metadata []string
	covers   []string
}

func (c *countingReader) Metadata(ctx context.Context, log *slog.Logger, src ebook.Source) (ebook.Metadata, error) {
	c.mu.Lock()
	c.metadata = append(c.metadata, src.Name)
	c.mu.Unlock()

	return c.Dispatcher.Metadata(ctx, log, src) //nolint:wrapcheck // test pass-through
}

func (c *countingReader) Cover(ctx context.Context, log *slog.Logger, src ebook.Source) ([]byte, error) {
	c.mu.Lock()
	c.covers = append(c.covers, src.Name)
	c.mu.Unlock()

	return c.Dispatcher.Cover(ctx, log, src) //nolint:wrapcheck // test pass-through
}

func (s *extractorSuite) SetupTest() {
	s.baseSuite.SetupTest()
	s.reader = &countingReader{Dispatcher: newTestDispatcher()}
}

func (s *extractorSuite) extractor() *Extractor {
	return NewExtractor(s.db, s.log, s.reader)
}

// fb2WithCover builds a minimal FB2 document carrying an annotation and an
// embedded JPEG cover.
func fb2WithCover(title, annotation string, cover []byte) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>%s</book-title>
      <author><first-name>A</first-name><last-name>B</last-name></author>
      <annotation>%s</annotation>
      <lang>en</lang>
      <coverpage><image href="#c.bin"/></coverpage>
    </title-info>
  </description>
  <binary id="c.bin" content-type="image/jpeg">%s</binary>
</FictionBook>`, title, annotation, base64.StdEncoding.EncodeToString(cover))
}

// insertBook inserts a book whose files are the given (source_path, format)
// pairs, in that order.
func (s *extractorSuite) insertBook(libraryID int64, files ...[2]string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: "T", LibraryID: libraryID, LibraryKey: files[0][0],
		Language: "en", ContentHash: files[0][0], AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	for _, f := range files {
		_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
			BookID: id, FileFormat: f[1], FileSize: 1, SourcePath: f[0],
		})
		s.Require().NoError(err)
	}

	return id
}

// ebookFixture returns a file of the ebook package's test data.
func (s *extractorSuite) ebookFixture(name string) []byte {
	data, err := os.ReadFile(filepath.Join("..", "ebook", "testdata", name))
	s.Require().NoError(err)

	return data
}

// fixtureLibrary syncs the read-only testdata/deepindex INPX library in place
// and returns it with its books keyed by title.
func (s *extractorSuite) fixtureLibrary() (dbq.Library, map[string]dbq.Book) {
	path, err := filepath.Abs(filepath.Join("testdata", "deepindex", "library.inpx"))
	s.Require().NoError(err)
	lib := s.insertLibrary("inpx", path)
	_, err = NewINPXParser(s.log, testRank).Sync(context.Background(), lib, s.db, s.store, nopReporter{})
	s.Require().NoError(err)

	return lib, s.booksByLibrary(lib.ID)
}

func (s *extractorSuite) TestCoverAndAnnotationFromFolder() {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(
		filepath.Join(dir, "a.fb2"),
		[]byte(fb2WithCover("Folder Book", "<p>Folder annotation</p>", []byte("FOLDER-COVER"))),
		0o600,
	))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, [2]string{"a.fb2", "fb2"})

	ext := s.extractor()

	cover, ok, err := ext.Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("FOLDER-COVER", string(cover))

	meta, ok, err := ext.Description(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Contains(meta.Annotation, "Folder annotation")
}

func (s *extractorSuite) TestDescriptionFromFolderEPUB() {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "a.epub"), s.ebookFixture("test.epub"), 0o600))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, [2]string{"a.epub", "epub"})

	meta, ok, err := s.extractor().Description(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("A test book for unit testing.", meta.Annotation)
}

func (s *extractorSuite) TestDescriptionFromArchiveEntries() {
	_, books := s.fixtureLibrary()
	cases := map[string]string{
		"Книга с аннотацией UTF-8": "Уникальнаяаннотация Звездолёт Бригантина", // 1.fb2
		"Книга в формате EPUB":     "Эпубное описание Маяк Верста",             // 7.epub
		"Книга в формате MOBI":     "Мобийное описание Дирижабль Заря",         // 8.mobi
	}
	ext := s.extractor()
	for title, want := range cases {
		book, ok := books[title]
		s.Require().True(ok, title)
		meta, ok, err := ext.Description(context.Background(), book.ID)
		s.Require().NoError(err, title)
		s.True(ok, title)
		s.Contains(meta.Annotation, want, title)
	}
}

func (s *extractorSuite) TestDescriptionSkipsPDFOnlyBook() {
	dir := s.T().TempDir()
	src := s.insertLibrary("folder", dir)
	// The PDF is never written to disk: opening it would fail. Descriptions
	// skip PDFs entirely, so the read returns cleanly without opening it.
	id := s.insertBook(src.ID, [2]string{"missing.pdf", "pdf"})

	ext := s.extractor()

	meta, ok, err := ext.Description(context.Background(), id)
	s.Require().NoError(err, "PDF must be skipped, not read")
	s.False(ok)
	s.Empty(meta.Annotation)
	s.Empty(s.reader.metadata, "no Metadata call for a PDF-only book")

	// Cover does read PDFs, so it actually tries to open the missing file.
	_, _, coverErr := ext.Cover(context.Background(), id)
	s.Error(coverErr)
}

func (s *extractorSuite) TestMultiFileBookReadsHighestRankedFile() {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "a.azw3"), s.ebookFixture("test.azw3"), 0o600))
	s.Require().NoError(os.WriteFile(
		filepath.Join(dir, "a.fb2"),
		[]byte(fb2WithCover("Ranked", "<p>Ranked FB2 annotation</p>", []byte("C"))),
		0o600,
	))
	src := s.insertLibrary("folder", dir)
	// The lower-ranked AZW3 is the first file by id and by format order.
	id := s.insertBook(src.ID, [2]string{"a.azw3", "azw3"}, [2]string{"a.fb2", "fb2"})

	meta, ok, err := s.extractor().Description(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Contains(meta.Annotation, "Ranked FB2 annotation")
	s.Equal([]string{"a.fb2"}, s.reader.metadata, "only the top-ranked file is read")
}

func (s *extractorSuite) TestCoverFromArchiveEPUB() {
	dir := s.T().TempDir()
	s.writeZip(filepath.Join(dir, "arc.zip"), "inner.epub", string(s.ebookFixture("test.epub")))
	src := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(src.ID, [2]string{"arc.zip/inner.epub", "epub"})

	cover, ok, err := s.extractor().Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.True(bytes.HasPrefix(cover, []byte("\x89PNG")), "the EPUB's PNG cover")
}

func (s *extractorSuite) TestCoverAndAnnotationFromArchiveFB2() {
	dir := s.T().TempDir()
	s.writeZip(filepath.Join(dir, "arc.zip"), "inner.fb2",
		fb2WithCover("Zip Book", "<p>Zip annotation</p>", []byte("ZIP-COVER")))

	// library.Path points at the .inpx; the archive is its sibling.
	src := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(src.ID, [2]string{"arc.zip/inner.fb2", "fb2"})

	ext := s.extractor()

	cover, ok, err := ext.Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("ZIP-COVER", string(cover))

	meta, ok, err := ext.Description(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Contains(meta.Annotation, "Zip annotation")
}

func (s *extractorSuite) TestEscapingSourcePathIsRejected() {
	dir := s.T().TempDir()
	root := filepath.Join(dir, "lib")
	s.Require().NoError(os.Mkdir(root, 0o755))
	s.Require().NoError(os.WriteFile(
		filepath.Join(dir, "x.fb2"),
		[]byte(fb2WithCover("Outside", "<p>outside</p>", []byte("C"))),
		0o600,
	))
	src := s.insertLibrary("folder", root)
	id := s.insertBook(src.ID, [2]string{"../x.fb2", "fb2"})

	_, ok, err := s.extractor().Description(context.Background(), id)
	s.Require().ErrorIs(err, errPathEscape)
	s.False(ok)
	s.Empty(s.reader.metadata, "the escaping file is never read")

	inpx := s.insertLibrary("inpx", filepath.Join(root, "lib.inpx"))
	inpxID := s.insertBook(inpx.ID, [2]string{"../arc.zip/x.fb2", "fb2"})
	_, _, err = s.extractor().Description(context.Background(), inpxID)
	s.Require().ErrorIs(err, errPathEscape)
}

func (s *extractorSuite) TestWithinRoot() {
	root := filepath.FromSlash("/lib/root")
	cases := map[string]bool{
		"a.fb2":           true,
		"sub/a.fb2":       true,
		"../x.fb2":        false,
		"sub/../../x.fb2": false,
		"..":              false,
		".":               false,
		"..x.fb2":         true,
	}
	for rel, want := range cases {
		s.Equal(want, withinRoot(root, filepath.Join(root, filepath.FromSlash(rel))), rel)
	}
}

func (s *extractorSuite) TestMissingArchiveEntry() {
	dir := s.T().TempDir()
	s.writeZip(filepath.Join(dir, "arc.zip"), "other.fb2", fb2WithCover("Z", "<p>z</p>", []byte("C")))
	src := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(src.ID, [2]string{"arc.zip/inner.fb2", "fb2"})

	_, ok, err := s.extractor().Description(context.Background(), id)
	s.Require().ErrorIs(err, errMissingEntry)
	s.False(ok)
}

func (s *extractorSuite) TestNothingIsWrittenUnderDataDir() {
	dataDir := s.T().TempDir()
	// Any temp file the reading path created would land here.
	s.T().Setenv("TMPDIR", dataDir)

	_, books := s.fixtureLibrary()
	ext := s.extractor()
	for _, title := range []string{"Книга с аннотацией UTF-8", "Книга в формате EPUB", "Книга в формате MOBI"} {
		id := books[title].ID
		_, _, err := ext.Description(context.Background(), id)
		s.Require().NoError(err, title)
		_, _, err = ext.Cover(context.Background(), id)
		s.Require().NoError(err, title)
	}

	entries, err := os.ReadDir(dataDir)
	s.Require().NoError(err)
	s.Empty(entries)
}

// TestArchiveReadErrorIsSourceIO pins that archiveCache reads archives through
// ebook.TagIO: a failing read of the archive while an entry inflates matches
// ebook.ErrSourceIO (retryable), not a corrupt entry.
func (s *extractorSuite) TestArchiveReadErrorIsSourceIO() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "arc.zip")
	s.writeZip(path, "inner.fb2", fb2WithCover("Z", "<p>z</p>", []byte("C")))

	ac := newArchiveCache()
	defer ac.close()
	f, err := ac.entry(path, "inner.fb2")
	s.Require().NoError(err)
	s.Require().NoError(ac.archives[path].file.Close()) // every later read fails

	src, closer := ebook.OpenZipEntry(f, ebook.MaxEntryBytes)
	defer func() { _ = closer.Close() }()
	_, err = src.R.ReadAt(make([]byte, 16), 0)
	s.Require().ErrorIs(err, ebook.ErrSourceIO)
	s.NotErrorIs(err, ebook.ErrMalformed)
}

func (s *extractorSuite) TestArchiveCacheOpensEachArchiveOnce() {
	lib, books := s.fixtureLibrary()
	ac := newArchiveCache()
	defer ac.close()
	res := newResolver()
	for _, title := range []string{"Книга в формате EPUB", "Книга в формате MOBI"} {
		file := s.fileFor(books[title].ID)
		src, closer, err := res.open(lib, file, ac)
		s.Require().NoError(err, title)
		s.Positive(src.Size, title)
		s.Require().NoError(closer.Close())
	}
	s.Len(ac.archives, 1, "both entries share one open archive")
}

func (s *extractorSuite) TestGroup() {
	res := newResolver()
	inpx := dbq.Library{Type: "inpx", Path: filepath.FromSlash("/lib/library.inpx")}
	s.Equal(filepath.FromSlash("/lib/a.zip"), res.group(inpx, dbq.BookFile{SourcePath: "a.zip/1.fb2"}))
	s.Empty(res.group(inpx, dbq.BookFile{SourcePath: "../a.zip/1.fb2"}), "an escaping archive has no group")
	folder := dbq.Library{Type: "folder", Path: filepath.FromSlash("/lib")}
	s.Empty(res.group(folder, dbq.BookFile{SourcePath: "a/1.fb2"}))
}

// TestDescriptionReturnsCleanedIdentifiers proves Description recovers identifiers
// from the source file and normalizes them like sync does (a messy ISBN-13
// with hyphens collapses to the canonical, hyphen-free form).
func (s *extractorSuite) TestDescriptionReturnsCleanedIdentifiers() {
	dir := s.T().TempDir()
	doc := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook>
  <description>
    <title-info>
      <book-title>Ided</book-title>
      <author><first-name>A</first-name><last-name>B</last-name></author>
      <lang>en</lang>
    </title-info>
    <publish-info><isbn>978-0-441-01359-3</isbn></publish-info>
  </description>
</FictionBook>`
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "a.fb2"), []byte(doc), 0o600))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, [2]string{"a.fb2", "fb2"})

	meta, ok, err := s.extractor().Description(context.Background(), id)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal([]ebook.Identifier{{Type: "isbn", Value: "9780441013593"}}, meta.Identifiers)
}

func (s *extractorSuite) TestMissingBookIsNotOK() {
	_, ok, err := s.extractor().Cover(context.Background(), 9999)
	s.Require().NoError(err)
	s.False(ok)
}

// TestCancelledReadOpensNoFile proves readBook tries no candidate once ctx is
// done, instead of opening each lower-ranked file of the book.
func (s *extractorSuite) TestCancelledReadOpensNoFile() {
	lib, books := s.fixtureLibrary()
	files := s.filesFor(books["Книга в формате EPUB"].ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ac := newArchiveCache()
	defer ac.close()
	_, ok, err := s.extractor().readBook(ctx, flowMetadata, lib, files, ac)
	s.Require().ErrorIs(err, context.Canceled)
	s.Empty(ac.archives, "no archive is opened")
	s.False(ok)
	s.Empty(s.reader.metadata)
}

func (s *extractorSuite) writeZip(path, name, content string) {
	f, err := os.Create(path)
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	entry, err := zw.Create(name)
	s.Require().NoError(err)
	_, err = io.WriteString(entry, content)
	s.Require().NoError(err)
	s.Require().NoError(zw.Close())
}
