package ingest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

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
	res, err := NewINPXParser(s.log, testRank).Sync(context.Background(), s.lib, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(10, res.Added)

	s.reader = &countingReader{Dispatcher: newTestDispatcher()}
	s.ext = NewExtractor(s.db, s.log, s.reader)
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

	s.Equal(DeepIndexResult{Checked: 8, Filled: 6, Skipped: 0, Errors: 2}, res)
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

	s.Equal(DeepIndexResult{Checked: 8, Filled: 5, Skipped: 1, Errors: 2}, res)
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

	s.Equal(2, res.Errors)
	for _, title := range []string{diCorrupt, diBadEPUB} {
		b := s.book(title)
		s.Equal(int64(0), b.MetadataChecked, title)
		s.False(b.Annotation.Valid, title)
	}
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

	s.Equal(DeepIndexResult{Errors: 2}, res)
	s.ElementsMatch([]string{"4.fb2", "9.epub"}, s.reads(), "only the still-unchecked entries are retried")
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

	// A later run resumes and finishes the remaining books.
	res2, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(8, checked+res2.Checked)
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
	s.Equal(DeepIndexResult{Checked: 7, Filled: 5, Errors: 3}, res)
	s.False(s.checkedTitles()[diBig])
	s.True(s.checkedTitles()[diPDF])
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
	d := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), NewExtractor(s.db, s.log, reader))
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
