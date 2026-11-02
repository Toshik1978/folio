package ingest

import (
	"archive/zip"
	"context"
	"path/filepath"
	"strings"
)

// deepIndexFixturesSuite proves the committed testdata/deepindex fixtures are
// valid input for the real INPX reader.
type deepIndexFixturesSuite struct {
	baseSuite
}

func (s *deepIndexFixturesSuite) TestINPXListsExpectedBooks() {
	path, err := filepath.Abs(filepath.Join("testdata", "deepindex", "library.inpx"))
	s.Require().NoError(err)

	src := s.insertLibrary("inpx", path)
	res, err := NewINPXParser(s.log, testRank).Sync(context.Background(), src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(10, res.Added)

	want := map[string]string{
		"Книга с аннотацией UTF-8":          "a.fb2-000001-000004.zip/1.fb2",
		"Книга в кодировке 1251":            "a.fb2-000001-000004.zip/2.fb2",
		"Книга без аннотации":               "a.fb2-000001-000004.zip/3.fb2",
		"Повреждённая книга":                "a.fb2-000001-000004.zip/4.fb2",
		"Книга с большой бинарной секцией":  "b.fb2-000005-000006.zip/5.fb2",
		"Книга только в PDF":                "b.fb2-000005-000006.zip/6.pdf",
		"Книга в формате EPUB":              "c.fb2-000007-000010.zip/7.epub",
		"Книга в формате MOBI":              "c.fb2-000007-000010.zip/8.mobi",
		"Усечённая книга EPUB":              "c.fb2-000007-000010.zip/9.epub",
		"Книга с огромной бинарной секцией": "c.fb2-000007-000010.zip/10.fb2",
	}
	books := s.booksByLibrary(src.ID)
	s.Require().Len(books, len(want))
	for title, source := range want {
		book, ok := books[title]
		s.Require().True(ok, title)
		s.Equal(source, s.fileFor(book.ID).SourcePath, title)
	}
}

func (s *deepIndexFixturesSuite) TestArchiveContents() {
	dir := filepath.Join("testdata", "deepindex")
	cases := map[string][]string{
		"a.fb2-000001-000004.zip": {"1.fb2", "2.fb2", "3.fb2", "4.fb2"},
		"b.fb2-000005-000006.zip": {"5.fb2", "6.pdf"},
		"c.fb2-000007-000010.zip": {"7.epub", "8.mobi", "9.epub", "10.fb2"},
	}
	for name, entries := range cases {
		zr, err := zip.OpenReader(filepath.Join(dir, name))
		s.Require().NoError(err, name)
		got := make([]string, 0, len(zr.File))
		sizes := map[string]uint64{}
		for _, f := range zr.File {
			got = append(got, f.Name)
			sizes[f.Name] = f.UncompressedSize64
		}
		_ = zr.Close()
		s.Equal(entries, got, name)
		if strings.HasPrefix(name, "b.") {
			s.Greater(sizes["5.fb2"], uint64(200*1024), "large binary entry")
		}
		if strings.HasPrefix(name, "c.") {
			s.Greater(sizes["10.fb2"], uint64(1<<20), "huge binary entry")
		}
	}
}
