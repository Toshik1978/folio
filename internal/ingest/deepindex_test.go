package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

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
)

// deepIndexSuite drives the DeepIndexer against a synced copy of the
// testdata/deepindex INPX library.
type deepIndexSuite struct {
	baseSuite

	dir    string // temp copy of the fixtures
	lib    dbq.Library
	ext    *countingExtractor
	parses atomic.Int64
}

// countingExtractor is a FileExtractor that records every call; the INPX path
// must never touch it for FB2/PDF entries.
type countingExtractor struct {
	mu     sync.Mutex
	calls  []int64
	meta   ebook.Metadata
	ok     bool
	errFor map[int64]error
}

func (c *countingExtractor) Backfill(_ context.Context, bookID int64) (ebook.Metadata, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, bookID)
	if err := c.errFor[bookID]; err != nil {
		return ebook.Metadata{}, false, err
	}

	return c.meta, c.ok, nil
}

func (c *countingExtractor) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.calls)
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
	s.parses.Store(0)

	s.dir = s.T().TempDir()
	for _, name := range []string{"library.inpx", "a.fb2-000001-000004.zip", "b.fb2-000005-000006.zip"} {
		data, err := os.ReadFile(filepath.Join("testdata", "deepindex", name))
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(filepath.Join(s.dir, name), data, 0o644))
	}

	s.lib = s.insertLibrary("inpx", filepath.Join(s.dir, "library.inpx"))
	res, err := NewINPXParser(s.log).Sync(context.Background(), s.lib, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Require().Equal(6, res.Added)

	s.ext = &countingExtractor{}
}

// indexer builds a DeepIndexer whose FB2 header parser counts its calls.
func (s *deepIndexSuite) indexer() *DeepIndexer {
	d := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), s.ext)
	d.parseFB2 = func(r io.Reader) (ebook.Metadata, error) {
		s.parses.Add(1)
		return ebook.ParseFB2Description(r)
	}

	return d
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

	s.Equal(DeepIndexResult{Checked: 5, Filled: 3, Skipped: 0, Errors: 1}, res)
	s.Equal(6, rep.total)
	s.Equal(6, rep.added)
	s.Zero(s.ext.callCount(), "INPX FB2/PDF books never go through the extractor")

	s.Equal(map[string]bool{
		diUTF8: true, diCP1251: true, diNoAnn: true, diCorrupt: false, diBig: true, diPDF: true,
	}, s.checkedTitles())

	utf := s.book(diUTF8)
	s.Contains(utf.Annotation.String, "Уникальнаяаннотация Звездолёт Бригантина")
	s.True(s.ftsHas(utf.ID, "annotation", "Бригантина"), "filled annotation is FTS searchable")
	s.Contains(s.book(diBig).Annotation.String, "Громадный Бинарник Мельница")
	s.False(s.book(diNoAnn).Annotation.Valid)
	s.False(s.book(diPDF).Annotation.Valid)
}

func (s *deepIndexSuite) TestExistingAnnotationIsNotOverwritten() {
	id := s.book(diUTF8).ID
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE books SET annotation = 'Ручное описание' WHERE id = ?`, id)
	s.Require().NoError(err)

	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Checked: 5, Filled: 2, Skipped: 1, Errors: 1}, res)
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

	s.Equal(1, res.Errors)
	b := s.book(diCorrupt)
	s.Equal(int64(0), b.MetadataChecked)
	s.False(b.Annotation.Valid)
}

func (s *deepIndexSuite) TestRerunDoesNoParsing() {
	_, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(int64(5), s.parses.Load(), "first run parses the five FB2 entries, never the PDF")

	s.parses.Store(0)
	res, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Errors: 1}, res)
	s.Equal(int64(1), s.parses.Load(), "only the still-unchecked corrupt entry is retried")
	s.Zero(s.ext.callCount())
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
	s.Less(checked, 5, "later batches were not committed")

	// A later run resumes and finishes the remaining books.
	res2, err := s.indexer().Run(context.Background(), s.lib, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)
	s.Equal(5, checked+res2.Checked)
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
	}, s.checkedTitles())
	s.False(s.book(diCP1251).Annotation.Valid)
}

func (s *deepIndexSuite) TestArchivesAreUnchanged() {
	type snap struct {
		info os.FileInfo
		data []byte
	}
	names := []string{"library.inpx", "a.fb2-000001-000004.zip", "b.fb2-000005-000006.zip"}
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
	s.Equal(DeepIndexResult{Checked: 4, Filled: 2, Errors: 2}, res)
	s.False(s.checkedTitles()[diBig])
	s.True(s.checkedTitles()[diPDF])
}

// deepIndexFolderSuite covers non-INPX libraries, which go per book through the
// local FileExtractor.
type deepIndexFolderSuite struct {
	baseSuite
}

func (s *deepIndexFolderSuite) seedBook(libraryID int64, title, format string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: title, LibraryID: libraryID, LibraryKey: title,
		Language: "en", ContentHash: title, AddedAt: 1,
	})
	s.Require().NoError(err)
	_, err = q.InsertBookFile(context.Background(), dbq.InsertBookFileParams{
		BookID: id, FileFormat: format, FileSize: 1, SourcePath: title + "." + format,
	})
	s.Require().NoError(err)

	return id
}

func (s *deepIndexFolderSuite) TestNonINPXUsesExtractor() {
	src := s.insertLibrary("folder", s.T().TempDir())
	epub := s.seedBook(src.ID, "Epub Book", "epub")
	pdf := s.seedBook(src.ID, "Pdf Book", "pdf")
	broken := s.seedBook(src.ID, "Broken Book", "fb2")

	ext := &countingExtractor{
		meta: ebook.Metadata{
			Annotation:  "<p>Локальное описание</p>",
			Identifiers: []ebook.Identifier{{Type: "isbn", Value: "9780441013593"}},
		},
		ok:     true,
		errFor: map[int64]error{broken: errors.New("unreadable")},
	}
	d := NewDeepIndexer(s.log, s.db, db.NewWriteGuard(), ext)
	res, err := d.Run(context.Background(), src, DeepIndexAll(), nopReporter{})
	s.Require().NoError(err)

	s.Equal(DeepIndexResult{Checked: 2, Filled: 1, Errors: 1}, res)
	s.ElementsMatch([]int64{epub, broken}, ext.calls, "PDF-only books are never handed to the extractor")

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
