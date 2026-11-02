package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
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
	return NewExtractor(s.db, s.log, s.reader, db.NewWriteGuard())
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
	_, err = NewINPXParser(s.log, newTestDispatcher()).Sync(context.Background(), lib, s.db, s.store, nopReporter{})
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

	meta, res := ext.Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
	s.Contains(meta.Annotation, "Folder annotation")
}

func (s *extractorSuite) TestDescriptionFromFolderEPUB() {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "a.epub"), s.ebookFixture("test.epub"), 0o600))
	src := s.insertLibrary("folder", dir)
	id := s.insertBook(src.ID, [2]string{"a.epub", "epub"})

	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
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
		meta, res := ext.Description(context.Background(), book.ID)
		s.Require().Equal(ReadDone, res.Status, title)
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

	meta, res := ext.Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, "PDF must be skipped, not read")
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

	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
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

	meta, res := ext.Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
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

	_, res := s.extractor().Description(context.Background(), id)
	s.Equal(ReadRetryable, res.Status)
	s.Require().ErrorIs(res.Err, errPathEscape)
	s.Empty(s.reader.metadata, "the escaping file is never read")

	inpx := s.insertLibrary("inpx", filepath.Join(root, "lib.inpx"))
	inpxID := s.insertBook(inpx.ID, [2]string{"../arc.zip/x.fb2", "fb2"})
	_, res = s.extractor().Description(context.Background(), inpxID)
	s.Require().ErrorIs(res.Err, errPathEscape)
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

	_, res := s.extractor().Description(context.Background(), id)
	s.Equal(ReadBroken, res.Status)
	s.Equal(ebook.ReasonMissingEntry, res.Reason)
	s.Require().ErrorIs(res.Err, errMissingEntry)
	s.NotEmpty(res.Fingerprint, "the archive's stat fingerprints a missing entry")
}

func (s *extractorSuite) TestNothingIsWrittenUnderDataDir() {
	dataDir := s.T().TempDir()
	// Any temp file the reading path created would land here.
	s.T().Setenv("TMPDIR", dataDir)

	_, books := s.fixtureLibrary()
	ext := s.extractor()
	for _, title := range []string{"Книга с аннотацией UTF-8", "Книга в формате EPUB", "Книга в формате MOBI"} {
		id := books[title].ID
		_, res := ext.Description(context.Background(), id)
		s.Require().Equal(ReadDone, res.Status, title)
		_, _, err := ext.Cover(context.Background(), id)
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
	f, _, err := ac.entry(path, "inner.fb2")
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

	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
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
	_, res := s.extractor().readBook(ctx, flowMetadata, lib, files, ac)
	s.Equal(ReadRetryable, res.Status)
	s.Require().ErrorIs(res.Err, context.Canceled)
	s.Empty(ac.archives, "no archive is opened")
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

// fingerprintOf returns the current metadata-flow fingerprint of a book.
func (s *extractorSuite) fingerprintOf(lib dbq.Library, bookID int64) string {
	ac := newArchiveCache()
	defer ac.close()
	fp, err := newResolver().fingerprint(lib, s.filesFor(bookID), ac)
	s.Require().NoError(err)

	return fp
}

// recordFailure stores a metadata-flow broken record for a book.
func (s *extractorSuite) recordFailure(bookID int64, fingerprint string, version int64) {
	err := dbq.New(s.db).UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: bookID, Flow: string(flowMetadata), Reason: string(ebook.ReasonCorrupt),
		Detail: "recorded", Fingerprint: fingerprint, ReaderVersion: version, RecordedAt: 1,
	})
	s.Require().NoError(err)
}

// folderBook writes a valid FB2 into a folder library and returns the library
// and the book.
func (s *extractorSuite) folderBook() (dbq.Library, int64) {
	dir := s.T().TempDir()
	s.Require().NoError(os.WriteFile(
		filepath.Join(dir, "a.fb2"),
		[]byte(fb2WithCover("Gated", "<p>Gated annotation</p>", []byte("C"))),
		0o600,
	))
	lib := s.insertLibrary("folder", dir)

	return lib, s.insertBook(lib.ID, [2]string{"a.fb2", "fb2"})
}

func (s *extractorSuite) TestApplicableRecordSkipsTheRead() {
	lib, id := s.folderBook()
	fp := s.fingerprintOf(lib, id)
	s.recordFailure(id, fp, ebook.ReaderVersion)

	_, res := s.extractor().Description(context.Background(), id)
	s.Equal(ReadBroken, res.Status)
	s.True(res.Known)
	s.Equal(ebook.ReasonCorrupt, res.Reason)
	s.Equal("recorded", res.Detail)
	s.Equal(fp, res.Fingerprint)
	s.Require().Error(res.Err)
	s.Empty(s.reader.metadata, "a known-broken book is not read")
}

func (s *extractorSuite) TestStaleVersionIsRead() {
	lib, id := s.folderBook()
	s.recordFailure(id, s.fingerprintOf(lib, id), ebook.ReaderVersion-1)

	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
	s.False(res.Known)
	s.Contains(meta.Annotation, "Gated annotation")
	s.Equal([]string{"a.fb2"}, s.reader.metadata)
}

func (s *extractorSuite) TestStaleFingerprintIsRead() {
	_, id := s.folderBook()
	s.recordFailure(id, "0123456789abcdef0123456789abcdef", ebook.ReaderVersion)

	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
	s.Contains(meta.Annotation, "Gated annotation")
	s.Equal([]string{"a.fb2"}, s.reader.metadata)
}

func (s *extractorSuite) TestBrokenFixturesAreCorrupt() {
	lib, books := s.fixtureLibrary()
	ext := s.extractor()
	for _, title := range []string{"Повреждённая книга", "Усечённая книга EPUB"} { // 4.fb2, 9.epub
		id := books[title].ID
		_, res := ext.Description(context.Background(), id)
		s.Equal(ReadBroken, res.Status, title)
		s.False(res.Known, title)
		s.Equal(ebook.ReasonCorrupt, res.Reason, title)
		s.Equal(s.fingerprintOf(lib, id), res.Fingerprint, title)
		s.NotEmpty(res.Detail, title)
		s.Require().ErrorIs(res.Err, ebook.ErrMalformed, title)
	}
}

func (s *extractorSuite) TestOversizedEntryIsTooLarge() {
	_, books := s.fixtureLibrary()
	ext := s.extractor()
	ext.res.entryLimit = 512 // 7.epub inflates to 941 bytes

	_, res := ext.Description(context.Background(), books["Книга в формате EPUB"].ID) // 7.epub
	s.Equal(ReadBroken, res.Status)
	s.Equal(ebook.ReasonTooLarge, res.Reason)
	s.Require().ErrorIs(res.Err, ebook.ErrTooLarge)
}

func (s *extractorSuite) TestRenamedAwayArchiveIsRetryable() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "arc.zip")
	s.writeZip(path, "inner.fb2", fb2WithCover("Z", "<p>z</p>", []byte("C")))
	lib := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(lib.ID, [2]string{"arc.zip/inner.fb2", "fb2"})
	s.Require().NoError(os.Rename(path, filepath.Join(dir, "gone.zip")))

	_, res := s.extractor().Description(context.Background(), id)
	s.Equal(ReadRetryable, res.Status)
	s.Empty(res.Reason)
	s.Require().ErrorIs(res.Err, os.ErrNotExist)
}

// TestCorruptArchiveIsBroken pins that an archive whose container cannot be
// read is broken corrupt, fingerprinted by the archive's stat so replacing it
// re-checks the book.
func (s *extractorSuite) TestCorruptArchiveIsBroken() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "arc.zip")
	s.Require().NoError(os.WriteFile(path, []byte("not a zip archive"), 0o600))
	lib := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(lib.ID, [2]string{"arc.zip/inner.fb2", "fb2"})

	_, res := s.extractor().Description(context.Background(), id)
	s.Equal(ReadBroken, res.Status)
	s.Equal(ebook.ReasonCorrupt, res.Reason)
	s.Require().ErrorIs(res.Err, ebook.ErrMalformed)
	s.Require().NotEmpty(res.Fingerprint)
	s.recordFailure(id, res.Fingerprint, ebook.ReaderVersion)

	_, res = s.extractor().Description(context.Background(), id)
	s.True(res.Known, "the unchanged archive is not re-read")

	s.writeZip(path, "inner.fb2", fb2WithCover("Z", "<p>Fixed annotation</p>", []byte("C")))
	meta, res := s.extractor().Description(context.Background(), id)
	s.Require().Equal(ReadDone, res.Status, res.Err)
	s.Contains(meta.Annotation, "Fixed annotation")
}

// coverBook copies an ebook fixture into a folder library and returns the
// library, the book and the copied file's path.
func (s *extractorSuite) coverBook(fixture, format string) (dbq.Library, int64, string) {
	dir := s.T().TempDir()
	path := filepath.Join(dir, fixture)
	s.Require().NoError(os.WriteFile(path, s.ebookFixture(fixture), 0o600))
	lib := s.insertLibrary("folder", dir)

	return lib, s.insertBook(lib.ID, [2]string{fixture, format}), path
}

// readFailures returns the book's stored broken-read records, by flow.
func (s *extractorSuite) readFailures(bookID int64) map[readFlow]dbq.BookReadFailure {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT flow, reason, fingerprint FROM book_read_failures WHERE book_id = ?", bookID)
	s.Require().NoError(err)
	defer func() { _ = rows.Close() }()
	out := map[readFlow]dbq.BookReadFailure{}
	for rows.Next() {
		var r dbq.BookReadFailure
		s.Require().NoError(rows.Scan(&r.Flow, &r.Reason, &r.Fingerprint))
		out[readFlow(r.Flow)] = r
	}
	s.Require().NoError(rows.Err())

	return out
}

// serveCover serves bookID's cover from store and returns the response body.
func (s *extractorSuite) serveCover(store *covers.Store, bookID int64) []byte {
	rr := httptest.NewRecorder()
	store.ServeCover(rr, httptest.NewRequestWithContext(context.Background(),
		http.MethodGet, "/cover", http.NoBody), bookID)
	s.Require().Equal(http.StatusOK, rr.Code)

	return rr.Body.Bytes()
}

// coverStateOf returns bookID's cover_state.
func (s *extractorSuite) coverStateOf(bookID int64) int64 {
	book, err := dbq.New(s.db).GetBook(context.Background(), bookID)
	s.Require().NoError(err)

	return book.CoverState
}

// TestBadCoverIsRecordedAndServedPlaceholder drives the lazy cover flow over a
// real Extractor: an undecodable FB2 cover is recorded once for the cover flow,
// the placeholder is served, cover_state stays unknown, the second serve is
// answered by the record without a read, and the book is not counted as
// unreadable by the metadata flow.
func (s *extractorSuite) TestBadCoverIsRecordedAndServedPlaceholder() {
	lib, id, _ := s.coverBook("badcover.fb2", "fb2")
	store, err := covers.NewStore(s.T().TempDir(), s.extractor(), NewCoverState(s.db))
	s.Require().NoError(err)
	placeholder := s.serveCover(store, -1) // no such book: the placeholder

	s.Equal(placeholder, s.serveCover(store, id))
	failures := s.readFailures(id)
	s.Require().Len(failures, 1)
	s.Equal(string(ebook.ReasonCorrupt), failures[flowCover].Reason)
	s.Zero(s.coverStateOf(id), "a broken cover leaves cover_state unknown")
	s.Equal([]string{"badcover.fb2"}, s.reader.covers)

	s.Equal(placeholder, s.serveCover(store, id))
	s.Equal([]string{"badcover.fb2"}, s.reader.covers, "a recorded cover is not re-read")
	s.Zero(s.coverStateOf(id))

	res, err := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), s.extractor()).
		Run(context.Background(), lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(1, res.Checked, "the metadata flow still reads the book")
	s.Zero(res.Broken+res.KnownBroken, "a cover failure is not counted as unreadable")
	s.NotContains(s.readFailures(id), flowMetadata)
}

func (s *extractorSuite) TestBadCoverEPUBIsRecorded() {
	lib, id, _ := s.coverBook("badcover.epub", "epub")

	_, ok, err := s.extractor().Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrMalformed)
	s.False(ok)
	failures := s.readFailures(id)
	s.Require().Len(failures, 1)
	s.Equal(s.fingerprintOf(lib, id), failures[flowCover].Fingerprint)
}

func (s *extractorSuite) TestKnownBadCoverIsMalformedWithoutRead() {
	_, id, _ := s.coverBook("badcover.fb2", "fb2")
	_, _, err := s.extractor().Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrMalformed)

	_, ok, err := s.extractor().Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrMalformed)
	s.Require().ErrorIs(err, errKnownBroken)
	s.False(ok)
	s.Len(s.reader.covers, 1, "a known broken cover is not read")
}

func (s *extractorSuite) TestNonImageCoverIsNone() {
	_, id, _ := s.coverBook("oddcover.fb2", "fb2")
	store, err := covers.NewStore(s.T().TempDir(), s.extractor(), NewCoverState(s.db))
	s.Require().NoError(err)

	s.serveCover(store, id)
	s.Empty(s.readFailures(id), "a non-image cover payload is none, not broken")
	s.EqualValues(2, s.coverStateOf(id))
}

func (s *extractorSuite) TestTouchedBadCoverIsReRead() {
	_, id, path := s.coverBook("badcover.fb2", "fb2")
	ext := s.extractor()
	_, _, err := ext.Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrMalformed)
	before := s.readFailures(id)[flowCover].Fingerprint

	later := time.Now().Add(time.Hour)
	s.Require().NoError(os.Chtimes(path, later, later))
	_, _, err = ext.Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrMalformed)
	s.Len(s.reader.covers, 2, "a new fingerprint re-reads the file")
	s.NotEqual(before, s.readFailures(id)[flowCover].Fingerprint)
}

func (s *extractorSuite) TestReadableCoverClearsStaleRecord() {
	_, id := s.folderBook()
	err := dbq.New(s.db).UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: id, Flow: string(flowCover), Reason: string(ebook.ReasonCorrupt),
		Detail: "stale", Fingerprint: "0123456789abcdef0123456789abcdef",
		ReaderVersion: ebook.ReaderVersion, RecordedAt: 1,
	})
	s.Require().NoError(err)

	data, ok, err := s.extractor().Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal([]byte("C"), data)
	s.Empty(s.readFailures(id), "a done cover read deletes the cover record")
}

func (s *extractorSuite) TestRetryableCoverErrorIsNotRecorded() {
	dir := s.T().TempDir()
	lib := s.insertLibrary("inpx", filepath.Join(dir, "lib.inpx"))
	id := s.insertBook(lib.ID, [2]string{"gone.zip/inner.fb2", "fb2"})

	_, _, err := s.extractor().Cover(context.Background(), id)
	s.Require().ErrorIs(err, os.ErrNotExist)
	s.Require().NotErrorIs(err, ebook.ErrMalformed)
	s.Empty(s.readFailures(id))
}

// TestDoneCoverWithoutRecordTakesNoGuard pins that a done cover read of a book
// with no stored record never waits for the write guard: there is nothing to
// delete, so a busy guard (a running sync) does not delay the cover.
func (s *extractorSuite) TestDoneCoverWithoutRecordTakesNoGuard() {
	_, id := s.folderBook()
	guard := db.NewWriteGuard()
	s.Require().NoError(guard.Lock(context.Background()))
	defer guard.Unlock()

	start := time.Now()
	data, ok, err := NewExtractor(s.db, s.log, s.reader, guard).Cover(context.Background(), id)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal([]byte("C"), data)
	s.Less(time.Since(start), recordAcquireBudget/2, "the cover is served without waiting for the guard")
}

// faultCoverReader is a bookReader whose Cover reads fail from offset from on,
// as a flaky disk would, through ebook.TagIO like a production source.
type faultCoverReader struct {
	*ebook.Dispatcher

	from int64
}

func (f faultCoverReader) Cover(ctx context.Context, log *slog.Logger, src ebook.Source) ([]byte, error) {
	src.R = ebook.TagIO(faultReaderAt{r: src.R, from: f.from})

	return f.Dispatcher.Cover(ctx, log, src) //nolint:wrapcheck // test pass-through
}

// faultReaderAt serves r until offset from, then fails with an I/O error.
type faultReaderAt struct {
	r    io.ReaderAt
	from int64
}

func (f faultReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.from {
		return 0, errors.New("EIO")
	}

	return f.r.ReadAt(p, off) //nolint:wrapcheck // test pass-through
}

// TestMOBICoverSourceIOLeavesStateUnknown: a failing read of a MOBI cover record
// is retryable, so the lazy cover flow serves the placeholder without marking
// the book "no cover" and records nothing.
func (s *extractorSuite) TestMOBICoverSourceIOLeavesStateUnknown() {
	_, id, _ := s.coverBook("test.mobi", "mobi")
	const coverRecordStart = 310 // record 1 of the test.mobi fixture
	ext := NewExtractor(s.db, s.log, faultCoverReader{Dispatcher: newTestDispatcher(), from: coverRecordStart},
		db.NewWriteGuard())

	_, ok, err := ext.Cover(context.Background(), id)
	s.Require().ErrorIs(err, ebook.ErrSourceIO)
	s.False(ok)

	store, err := covers.NewStore(s.T().TempDir(), ext, NewCoverState(s.db))
	s.Require().NoError(err)
	s.serveCover(store, id)
	s.Zero(s.coverStateOf(id), "a retryable read must not mark the book as having no cover")
	s.Empty(s.readFailures(id), "a retryable read records nothing")
}
