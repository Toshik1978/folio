package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// Fixture book titles (see testdata/deepindex/gen.go).
const (
	diUTF8    = "Книга с аннотацией UTF-8"
	diCP1251  = "Книга в кодировке 1251"
	diNoAnn   = "Книга без аннотации"
	diCorrupt = "Повреждённая книга"
	diBig     = "Книга с большой бинарной секцией"
	diPDF     = "Книга только в PDF"
	diEPUB    = "Книга в формате EPUB"
	diMOBI    = "Книга в формате MOBI"
	diBadEPUB = "Усечённая книга EPUB"
	diHuge    = "Книга с огромной бинарной секцией"
)

// diArchives returns the fixture files of the testdata/deepindex library.
func diArchives() []string {
	return []string{
		"library.inpx", "a.fb2-000001-000004.zip", "b.fb2-000005-000006.zip", "c.fb2-000007-000010.zip",
	}
}

// deepIndexSuite drives the DeepIndexer against a synced copy of the
// testdata/deepindex INPX library, reading through a real Extractor over the
// standard Dispatcher wrapped in a countingReader.
type deepIndexSuite struct {
	baseSuite

	dir    string // temp copy of the fixtures
	lib    dbq.Library
	reader *countingReader
	ext    *Extractor
}

// recordingReporter records progress; onAdd (optional) runs after each Add.
type recordingReporter struct {
	total int
	added int
	onAdd func()
}

func (r *recordingReporter) SetTotal(n int) { r.total = n }
func (r *recordingReporter) Add(n int) {
	r.added += n
	if r.onAdd != nil {
		r.onAdd()
	}
}

func (s *deepIndexSuite) SetupTest() {
	s.baseSuite.SetupTest()

	s.dir = s.T().TempDir()
	for _, name := range diArchives() {
		data, err := os.ReadFile(filepath.Join("testdata", "deepindex", name))
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(filepath.Join(s.dir, name), data, 0o644))
	}

	s.lib = s.insertLibrary("inpx", filepath.Join(s.dir, "library.inpx"))
	res, err := NewINPXParser(
		s.log,
		newTestDispatcher(),
	).Sync(context.Background(), s.lib, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(10, res.Added)

	s.reader = &countingReader{Dispatcher: newTestDispatcher()}
	s.ext = NewExtractor(s.db, s.log, s.reader, db.NewWriteGuard())
}

// indexer builds a DeepIndexer over the suite's Extractor.
func (s *deepIndexSuite) indexer() *DeepIndexer {
	return NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), s.ext)
}

// reads returns the names of the Sources read for metadata so far.
func (s *deepIndexSuite) reads() []string {
	s.reader.mu.Lock()
	defer s.reader.mu.Unlock()

	return slices.Clone(s.reader.metadata)
}

func (s *deepIndexSuite) book(title string) dbq.Book {
	b, ok := s.booksByLibrary(s.lib.ID)[title]
	s.Require().True(ok, title)

	return b
}

func (s *deepIndexSuite) checkedTitles() map[string]bool {
	out := map[string]bool{}
	books := s.booksByLibrary(s.lib.ID)
	for title := range books {
		out[title] = books[title].MetadataChecked == 1
	}

	return out
}

func (s *deepIndexSuite) TestCountsAreCorrect() {
	rep := &recordingReporter{}
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), rep)
	s.Require().NoError(err)

	s.Len(res.NewlyBroken, 2)
	res.NewlyBroken = nil
	s.Equal(DeepIndexResult{Checked: 8, Filled: 6, Skipped: 0, Broken: 2}, res)
	s.Equal(10, rep.total)
	s.Equal(10, rep.added)

	s.Equal(map[string]bool{
		diUTF8: true, diCP1251: true, diNoAnn: true, diCorrupt: false, diBig: true, diPDF: true,
		diEPUB: true, diMOBI: true, diBadEPUB: false, diHuge: true,
	}, s.checkedTitles())

	utf := s.book(diUTF8)
	s.Contains(utf.Annotation.String, "Уникальнаяаннотация Звездолёт Бригантина")
	s.True(s.ftsHas(utf.ID, "annotation", "Бригантина"), "filled annotation is FTS searchable")
	s.Contains(s.book(diBig).Annotation.String, "Громадный Бинарник Мельница")
	s.False(s.book(diNoAnn).Annotation.Valid)
	s.False(s.book(diPDF).Annotation.Valid)
}

// TestEveryFormatIsFilled proves EPUB and MOBI entries of an archive are read
// like FB2 ones (SC-003), and that a PDF-only book is checked without a read.
func (s *deepIndexSuite) TestEveryFormatIsFilled() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	epub := s.book(diEPUB)
	s.Contains(epub.Annotation.String, "Эпубное описание Маяк Верста")
	s.True(s.ftsHas(epub.ID, "annotation", "Верста"))
	mobi := s.book(diMOBI)
	s.Contains(mobi.Annotation.String, "Мобийное описание Дирижабль Заря")
	s.True(s.ftsHas(mobi.ID, "annotation", "Дирижабль"))
	s.Contains(s.book(diHuge).Annotation.String, "Исполинский Бинарник Маяк")

	s.Equal(int64(1), s.book(diPDF).MetadataChecked)
	s.NotContains(s.reads(), "6.pdf", "a PDF-only book is never read")
}

// TestFB2ReadStaysHeaderOnly caps every inflated archive entry at 64 KiB: the
// >1 MiB 10.fb2 is still filled, so its read never inflates past its header.
func (s *deepIndexSuite) TestFB2ReadStaysHeaderOnly() {
	s.ext.res.entryLimit = 64 << 10

	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	huge := s.book(diHuge)
	s.Equal(int64(1), huge.MetadataChecked)
	s.Contains(huge.Annotation.String, "Исполинский Бинарник Маяк")
}

// TestEachArchiveIsOpenedOncePerJob counts archive opens: every archive is one
// job, so each is opened exactly once in a run (b only for its FB2 entry).
func (s *deepIndexSuite) TestEachArchiveIsOpenedOncePerJob() {
	var mu sync.Mutex
	opens := map[string]int{}
	s.ext.onArchiveOpen = func(path string) {
		mu.Lock()
		defer mu.Unlock()
		opens[filepath.Base(path)]++
	}

	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(map[string]int{
		"a.fb2-000001-000004.zip": 1, "b.fb2-000005-000006.zip": 1, "c.fb2-000007-000010.zip": 1,
	}, opens)
}

func (s *deepIndexSuite) TestExistingAnnotationIsNotOverwritten() {
	id := s.book(diUTF8).ID
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE books SET annotation = 'Ручное описание' WHERE id = ?`, id)
	s.Require().NoError(err)

	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Len(res.NewlyBroken, 2)
	res.NewlyBroken = nil
	s.Equal(DeepIndexResult{Checked: 8, Filled: 5, Skipped: 1, Broken: 2}, res)
	got := s.book(diUTF8)
	s.Equal("Ручное описание", got.Annotation.String)
	s.Equal(int64(1), got.MetadataChecked, "a skipped book is still marked checked")
}

func (s *deepIndexSuite) TestWindows1251TextIsStoredCorrectly() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	b := s.book(diCP1251)
	s.Contains(b.Annotation.String, "Кириллица Самовар Лукоморье")
	s.True(s.ftsHas(b.ID, "annotation", "Лукоморье"))
}

func (s *deepIndexSuite) TestCorruptEntryStaysUnchecked() {
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(2, res.Broken)
	s.Zero(res.Errors)
	for _, title := range []string{diCorrupt, diBadEPUB} {
		b := s.book(title)
		s.Equal(int64(0), b.MetadataChecked, title)
		s.False(b.Annotation.Valid, title)
		rec, ok := s.failure(title)
		s.Require().True(ok, "a broken book is recorded: %s", title)
		s.Equal(int64(ebook.ReaderVersion), rec.ReaderVersion, title)
		s.NotEmpty(rec.Fingerprint, title)
		s.NotEmpty(rec.Reason, title)
	}
	s.Equal(2, s.failureCount())
}

func (s *deepIndexSuite) TestRerunDoesNoParsing() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Len(s.reads(), 9, "first run reads the nine describing entries, never the PDF")

	s.reader.mu.Lock()
	s.reader.metadata = nil
	s.reader.mu.Unlock()
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{KnownBroken: 2}, res, "SC-002: no errors, nothing newly broken")
	s.Empty(s.reads(), "SC-004: no entry is opened for a book known to be broken")
	s.Equal(2, s.failureCount())
}

// TestFixedEntryIsReread replaces archive a with one whose 4.fb2 parses: the
// changed fingerprint lifts the record, so the book is re-read, filled and
// its record deleted.
func (s *deepIndexSuite) TestFixedEntryIsReread() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	_, ok := s.failure(diCorrupt)
	s.Require().True(ok)

	s.fixArchiveA()
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Checked: 1, Filled: 1, KnownBroken: 1}, res)
	fixed := s.book(diCorrupt)
	s.Equal(int64(1), fixed.MetadataChecked)
	s.Contains(fixed.Annotation.String, "Уникальнаяаннотация")
	_, ok = s.failure(diCorrupt)
	s.False(ok, "the record of a book read since is deleted")
	_, ok = s.failure(diBadEPUB)
	s.True(ok, "an unchanged broken book keeps its record")
}

// fixArchiveA rewrites archive a with 4.fb2 replaced by a copy of the valid
// 1.fb2, under a newer entry mtime.
func (s *deepIndexSuite) fixArchiveA() {
	s.replaceEntry("a.fb2-000001-000004.zip", "4.fb2", "1.fb2")
}

// replaceEntry rewrites the fixture archive with entry target replaced by a
// copy of entry source, every entry under a newer mtime. The .inpx is left
// untouched.
func (s *deepIndexSuite) replaceEntry(archive, target, source string) {
	path := filepath.Join(s.dir, archive)
	zr, err := zip.OpenReader(path)
	s.Require().NoError(err)
	entries := map[string][]byte{}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		rc, openErr := f.Open()
		s.Require().NoError(openErr)
		data, readErr := io.ReadAll(rc)
		s.Require().NoError(readErr)
		_ = rc.Close()
		entries[f.Name] = data
		names = append(names, f.Name)
	}
	s.Require().NoError(zr.Close())
	entries[target] = entries[source]

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, createErr := zw.CreateHeader(&zip.FileHeader{
			Name: name, Method: zip.Deflate, Modified: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		})
		s.Require().NoError(createErr)
		_, err = w.Write(entries[name])
		s.Require().NoError(err)
	}
	s.Require().NoError(zw.Close())
	s.Require().NoError(os.WriteFile(path, buf.Bytes(), 0o644))
}

// failure returns the metadata-flow read failure record of the titled book.
func (s *deepIndexSuite) failure(title string) (dbq.BookReadFailure, bool) {
	rec, err := dbq.New(s.db).GetReadFailure(context.Background(),
		dbq.GetReadFailureParams{BookID: s.book(title).ID, Flow: string(flowMetadata)})
	if errors.Is(err, sql.ErrNoRows) {
		return dbq.BookReadFailure{}, false
	}
	s.Require().NoError(err)

	return rec, true
}

// failureCount returns the number of stored read failure records.
func (s *deepIndexSuite) failureCount() int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM book_read_failures`).Scan(&n))

	return n
}

func (s *deepIndexSuite) TestCancelledRunKeepsEarlierBatches() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := s.indexer()
	d.batchSize = 2
	rep := &recordingReporter{onAdd: cancel}
	res, err := d.Run(ctx, s.lib, DeepIndexAll(), rep)
	s.Require().ErrorIs(err, context.Canceled)

	checked := 0
	for _, ok := range s.checkedTitles() {
		if ok {
			checked++
		}
	}
	s.Equal(2, rep.added, "exactly one batch committed before the cancel")
	s.Equal(res.Checked, checked, "result reflects the committed batch")
	s.Positive(checked, "the first batch stays committed")
	s.Less(checked, 8, "later batches were not committed")
	s.Equal(res.Broken, s.failureCount(), "only the committed batch records broken books")

	// A later run resumes and finishes the remaining books.
	res2, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(8, checked+res2.Checked)
}

// TestPagedRunMatchesOneRun runs with pages of three books, so the ten books
// span four pages and archives straddle page boundaries: the result, the
// progress and the checked books are those of a single-page run.
func (s *deepIndexSuite) TestPagedRunMatchesOneRun() {
	d := s.indexer()
	d.pageSize = 3
	rep := &recordingReporter{}
	res, err := d.Run(context.Background(), s.lib, DeepIndexAll(), rep)
	s.Require().NoError(err)

	s.Len(res.NewlyBroken, 2)
	res.NewlyBroken = nil
	s.Equal(DeepIndexResult{Checked: 8, Filled: 6, Broken: 2}, res)
	s.Equal(10, rep.total)
	s.Equal(10, rep.added)
	s.Equal(map[string]bool{
		diUTF8: true, diCP1251: true, diNoAnn: true, diCorrupt: false, diBig: true, diPDF: true,
		diEPUB: true, diMOBI: true, diBadEPUB: false, diHuge: true,
	}, s.checkedTitles())
	s.Len(s.reads(), 9, "every book is read once; the PDF-only book is checked unread")
}

// TestCancelBetweenPagesStopsTheRun cancels after the first page's batch: the
// next page is not loaded, and a later run finishes the rest.
func (s *deepIndexSuite) TestCancelBetweenPagesStopsTheRun() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := s.indexer()
	d.pageSize = 3
	rep := &recordingReporter{onAdd: cancel}
	res, err := d.Run(ctx, s.lib, DeepIndexAll(), rep)
	s.Require().ErrorIs(err, context.Canceled)
	s.Equal(3, rep.added, "only the first page committed")

	res2, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(8, res.Checked+res2.Checked)
}

func (s *deepIndexSuite) TestImportedAtScopeTouchesOnlyMatchingBooks() {
	const stamp = int64(111)
	ids := []int64{s.book(diUTF8).ID, s.book(diNoAnn).ID}
	for _, id := range ids {
		_, err := s.db.ExecContext(context.Background(), `UPDATE books SET imported_at = ? WHERE id = ?`, stamp, id)
		s.Require().NoError(err)
	}

	rep := &recordingReporter{}
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexImportedAt(stamp), rep)
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Checked: 2, Filled: 1}, res)
	s.Equal(2, rep.total)
	s.Equal(map[string]bool{
		diUTF8: true, diCP1251: false, diNoAnn: true, diCorrupt: false, diBig: false, diPDF: false,
		diEPUB: false, diMOBI: false, diBadEPUB: false, diHuge: false,
	}, s.checkedTitles())
	s.False(s.book(diCP1251).Annotation.Valid)
}

func (s *deepIndexSuite) TestArchivesAreUnchanged() {
	type snap struct {
		info os.FileInfo
		data []byte
	}
	names := diArchives()
	before := map[string]snap{}
	for _, n := range names {
		p := filepath.Join(s.dir, n)
		info, err := os.Stat(p)
		s.Require().NoError(err)
		data, err := os.ReadFile(p)
		s.Require().NoError(err)
		before[n] = snap{info, data}
	}

	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	for _, n := range names {
		p := filepath.Join(s.dir, n)
		info, statErr := os.Stat(p)
		s.Require().NoError(statErr)
		s.Equal(before[n].info.ModTime(), info.ModTime(), n)
		s.Equal(before[n].info.Size(), info.Size(), n)
		data, readErr := os.ReadFile(p)
		s.Require().NoError(readErr)
		s.True(bytes.Equal(before[n].data, data), n)
	}
	entries, err := os.ReadDir(s.dir)
	s.Require().NoError(err)
	s.Len(entries, len(names), "nothing written next to the library")
}

func (s *deepIndexSuite) TestMissingArchiveCountsAsErrors() {
	s.Require().NoError(os.Remove(filepath.Join(s.dir, "b.fb2-000005-000006.zip")))

	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	// Archive b holds book 5 (FB2, unreadable now) and book 6 (PDF-only, which
	// needs no read and is still marked checked).
	s.Len(res.NewlyBroken, 2)
	res.NewlyBroken = nil
	s.Equal(DeepIndexResult{Checked: 7, Filled: 5, Broken: 2, Errors: 1}, res)
	s.False(s.checkedTitles()[diBig])
	s.True(s.checkedTitles()[diPDF])
	_, ok := s.failure(diBig)
	s.False(ok, "an unreadable archive is retryable and records nothing")
	s.Equal(2, s.failureCount())
}

// TestCancelledBeforeReadAddsNoRows cancels the run before any batch commits.
func (s *deepIndexSuite) TestCancelledBeforeReadAddsNoRows() {
	ctx, cancel := context.WithCancel(context.Background())
	d := s.indexer()
	rep := &recordingReporter{}
	s.ext.onArchiveOpen = func(string) { cancel() }

	res, err := d.Run(ctx, s.lib, DeepIndexAll(), rep)
	s.Require().ErrorIs(err, context.Canceled)

	s.Equal(DeepIndexResult{}, res)
	s.Zero(s.failureCount(), "a cancelled run records no broken book")
}

// deepIndexFolderSuite covers non-archive libraries, which go one book per job
// through the same Extractor read.
type deepIndexFolderSuite struct {
	baseSuite
}

// scriptedReader is a bookReader over the standard Dispatcher whose Metadata
// returns meta, or errFor[name] for the named Sources, recording every read.
type scriptedReader struct {
	*ebook.Dispatcher

	meta   ebook.Metadata
	errFor map[string]error

	mu    sync.Mutex
	names []string
}

func (r *scriptedReader) Metadata(_ context.Context, _ *slog.Logger, src ebook.Source) (ebook.Metadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, src.Name)
	if err := r.errFor[src.Name]; err != nil {
		return ebook.Metadata{}, err
	}

	return r.meta, nil
}

// seedBook inserts a book with one file, and writes that file under dir.
func (s *deepIndexFolderSuite) seedBook(libraryID int64, dir, title, format string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: title, LibraryID: libraryID, LibraryKey: title,
		Language: "en", ContentHash: title, AddedAt: 1,
	})
	s.Require().NoError(err)
	name := title + "." + format
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: format, FileSize: 1, SourcePath: name,
	})
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))

	return id
}

// TestNewlyBrokenSampleIsCapped runs over 7 newly broken books: Broken is the
// total, the sample holds 5 with their reasons, and a re-run reports none.
func (s *deepIndexFolderSuite) TestNewlyBrokenSampleIsCapped() {
	dir := s.T().TempDir()
	src := s.insertLibrary("folder", dir)

	reasons := map[int64]ebook.Reason{}
	errFor := map[string]error{}
	for i := range 7 {
		title := fmt.Sprintf("Broken %d", i)
		id := s.seedBook(src.ID, dir, title, "fb2")
		if i%2 == 0 {
			errFor[title+".fb2"], reasons[id] = fmt.Errorf("bad: %w", ebook.ErrMalformed), ebook.ReasonCorrupt
		} else {
			errFor[title+".fb2"], reasons[id] = fmt.Errorf("big: %w", ebook.ErrTooLarge), ebook.ReasonTooLarge
		}
	}
	reader := &scriptedReader{Dispatcher: newTestDispatcher(), errFor: errFor}
	d := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), NewExtractor(s.db, s.log, reader, db.NewWriteGuard()))

	res, err := d.Run(context.Background(), src, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(7, res.Broken)
	s.Require().Len(res.NewlyBroken, brokenSampleSize)
	seen := map[int64]bool{}
	for _, b := range res.NewlyBroken {
		s.Equal(reasons[b.BookID], b.Reason)
		s.False(seen[b.BookID], "no duplicates")
		seen[b.BookID] = true
	}

	res, err = d.Run(context.Background(), src, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Zero(res.Broken)
	s.Equal(7, res.KnownBroken)
	s.Empty(res.NewlyBroken)
}

func (s *deepIndexFolderSuite) TestFolderLibraryReadsThroughExtractor() {
	dir := s.T().TempDir()
	src := s.insertLibrary("folder", dir)
	epub := s.seedBook(src.ID, dir, "Epub Book", "epub")
	pdf := s.seedBook(src.ID, dir, "Pdf Book", "pdf")
	broken := s.seedBook(src.ID, dir, "Broken Book", "fb2")

	reader := &scriptedReader{
		Dispatcher: newTestDispatcher(),
		meta: ebook.Metadata{
			Annotation:  "<p>Локальное описание</p>",
			Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
		},
		errFor: map[string]error{"Broken Book.fb2": errors.New("unreadable")},
	}
	d := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), NewExtractor(s.db, s.log, reader, db.NewWriteGuard()))
	res, err := d.Run(context.Background(), src, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Checked: 2, Filled: 1, Errors: 1}, res)
	s.ElementsMatch([]string{"Epub Book.epub", "Broken Book.fb2"}, reader.names, "PDF-only books are never read")

	q := dbq.New(s.db)
	got, err := q.GetBook(context.Background(), epub)
	s.Require().NoError(err)
	s.Equal("<p>Локальное описание</p>", got.Annotation.String)
	s.Equal(int64(1), got.MetadataChecked)
	ids, err := q.ListIdentifiersForBook(context.Background(), epub)
	s.Require().NoError(err)
	s.Require().Len(ids, 1)
	s.Equal("9780441013593", ids[0].Value)

	got, err = q.GetBook(context.Background(), pdf)
	s.Require().NoError(err)
	s.Equal(int64(1), got.MetadataChecked)
	got, err = q.GetBook(context.Background(), broken)
	s.Require().NoError(err)
	s.Equal(int64(0), got.MetadataChecked)
}

func (s *deepIndexSuite) TestBrokenLogCarriesDetail() {
	log, recs := capturedLog()
	d := NewDeepIndexer(log, s.db, db.NewWriteGuard(), s.ext)
	res, err := d.Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(2, res.Broken)

	n := 0
	for i := range *recs {
		if (*recs)[i].Message != "unreadable book file" {
			continue
		}
		n++
		var detail string
		(*recs)[i].Attrs(func(a slog.Attr) bool {
			if a.Key == "detail" {
				detail = a.Value.String()
			}

			return true
		})
		s.NotEmpty(detail, "the unreadable-file log must carry the failure detail")
	}
	s.Equal(2, n, "one log per broken book")
}
