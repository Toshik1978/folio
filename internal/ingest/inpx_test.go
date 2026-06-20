package ingest

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type inpxSuite struct {
	baseSuite
}

// inpLine builds one real-shaped 15-field .inp record (the trailing fields are
// LIBID=FILE, DATE, RATE, KEYWORDS). author/genre tokens include their
// trailing ':' as in real data.
func inpLine(author, genre, title, series, serno, file, size, del, lang string) string {
	fields := []string{
		author, genre, title, series, serno,
		file, size, file, del, "fb2",
		"2008-07-05", lang, "3", "",
	}

	return strings.Join(fields, "\x04") + "\x04"
}

func (s *inpxSuite) writeINPX(path string, files map[string]string) {
	f, err := os.Create(path)
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		s.Require().NoError(err)
		_, err = w.Write([]byte(content))
		s.Require().NoError(err)
	}
	s.Require().NoError(zw.Close())
}

func (s *inpxSuite) TestSync() {
	ctx := context.Background()
	dir := s.T().TempDir()

	// Two .inp index files, each mapping to its own sibling .zip archive.
	// Records use the real 15-field, UTF-8 flibusta layout.
	arc1 := inpLine(
		"Громов,Александр,Николаевич:",
		"sf_social:",
		"Первый из могикан",
		"Мир матриархата",
		"2",
		"55",
		"102400",
		"0",
		"ru",
	) +
		"\r\n" +
		// deleted (DEL=1) -> skipped
		inpLine(
			"Дой,Джон,:",
			"sf:",
			"Удалённая книга",
			"",
			"",
			"60",
			"100",
			"1",
			"en",
		)
	arc2 := strings.Join([]string{
		inpLine("Толстой,Лев,Николаевич:", "prose_classic:", "Война и мир", "", "", "161", "200", "0", "ru"),
		"too\x04few", // malformed -> skipped
		"",
	}, "\r\n")

	inpxPath := filepath.Join(dir, "library.inpx")
	s.writeINPX(inpxPath, map[string]string{
		"d.fb2-000001-000100.inp": arc1,
		"f.fb2-000101-000200.inp": arc2,
	})

	src := s.insertLibrary("inpx", inpxPath)
	parser := INPXParser{s.log}
	res, err := parser.Sync(ctx, src, s.db, s.store, nopReporter{})
	s.Require().NoError(err)
	s.Equal(2, res.Added)

	books := s.booksByLibrary(src.ID)
	s.Require().Len(books, 2)

	mogikan := books["Первый из могикан"]
	mogikanFile := s.fileFor(mogikan.ID)
	s.Equal("d.fb2-000001-000100.zip/55.fb2", mogikanFile.SourcePath)
	s.Equal("fb2", mogikanFile.FileFormat)
	s.Equal(int64(102400), mogikanFile.FileSize)
	s.Equal("ru", mogikan.Language)
	s.Require().True(mogikan.SeriesID.Valid)
	s.Require().True(mogikan.SeriesNumber.Valid)
	s.InDelta(2.0, mogikan.SeriesNumber.Float64, 0.0001)
	s.False(mogikan.Annotation.Valid, "INPX import carries no annotation")

	voina := books["Война и мир"]
	s.Equal("f.fb2-000101-000200.zip/161.fb2", s.fileFor(voina.ID).SourcePath)
	s.False(voina.SeriesID.Valid)

	authors, err := dbq.New(s.db).ListAuthors(ctx, dbq.ListAuthorsParams{Lim: 1000})
	s.Require().NoError(err)
	names := make([]string, len(authors))
	for i := range authors {
		names[i] = authors[i].Name
	}
	s.ElementsMatch([]string{"Александр Николаевич Громов", "Лев Николаевич Толстой"}, names)

	genres, err := dbq.New(s.db).ListGenres(ctx, dbq.ListGenresParams{LibraryID: 0, Lim: 1000, Off: 0})
	s.Require().NoError(err)
	gnames := make([]string, len(genres))
	for i := range genres {
		gnames[i] = genres[i].Name
	}
	s.ElementsMatch([]string{"Science Fiction", "Classics"}, gnames)
}

func (s *inpxSuite) TestParseLineDate() {
	// inpLine sets the date field (index 10) to "2008-07-05".
	rec, ok := parseINPLine(inpLine("A,B,:", "sf:", "T", "", "", "9", "100", "0", "ru"), 1, "arc.zip")
	s.Require().True(ok)
	s.Equal(int64(1215216000), rec.AddedAt, "2008-07-05 → unix")

	// A record truncated before the date field carries no AddedAt.
	short := "A,B,:\x04sf\x04T\x04\x04\x049\x04100\x049\x040\x04fb2"
	rec, ok = parseINPLine(short, 1, "arc.zip")
	s.Require().True(ok)
	s.Zero(rec.AddedAt)
}

func (s *inpxSuite) TestParseLine() {
	tests := []struct {
		name    string
		line    string
		wantOK  bool
		title   string
		authors []string
		genres  []string
		series  string
		sernoOK bool
		serno   float64
		lang    string
		path    string
	}{
		{
			name:    "valid",
			line:    "Tolstoy,Lev,Nikolaevich:\x04prose\x04War and Peace\x04Saga\x042\x0455\x04100\x0455\x040\x04fb2\x042020\x04ru\x04",
			wantOK:  true,
			title:   "War and Peace",
			authors: []string{"Lev Nikolaevich Tolstoy"},
			genres:  []string{"prose"},
			series:  "Saga",
			sernoOK: true,
			serno:   2,
			lang:    "ru",
			path:    "arc.zip/55.fb2",
		},
		{
			name: "full utf8 record",
			line: inpLine(
				"Громов,Александр,Николаевич:",
				"sf_social:sf:",
				"Первый из могикан",
				"Мир матриархата",
				"2",
				"55",
				"102400",
				"0",
				"ru",
			),
			wantOK:  true,
			title:   "Первый из могикан",
			authors: []string{"Александр Николаевич Громов"},
			genres:  []string{"sf_social", "sf"},
			series:  "Мир матриархата",
			sernoOK: true,
			serno:   2,
			lang:    "ru",
			path:    "arc.zip/55.fb2",
		},
		{
			name:    "multiple authors",
			line:    "Tolstoy,Lev,:Pushkin,Alexander,Sergeevich:\x04\x04Book\x04\x04\x041\x040\x041\x040\x04fb2\x04\x04\x04",
			wantOK:  true,
			title:   "Book",
			authors: []string{"Lev Tolstoy", "Alexander Sergeevich Pushkin"},
			path:    "arc.zip/1.fb2",
		},
		{
			name:    "series without number",
			line:    "A,B,:\x04\x04Standalone\x04Some Series\x04\x047\x04100\x047\x040\x04fb2\x04\x04ru\x04",
			wantOK:  true,
			title:   "Standalone",
			authors: []string{"B A"},
			series:  "Some Series",
			sernoOK: false,
			lang:    "ru",
			path:    "arc.zip/7.fb2",
		},
		{name: "deleted", line: "A,B,:\x04\x04T\x04\x04\x041\x040\x041\x041\x04fb2\x04\x04\x04", wantOK: false},
		// L9: a padded DEL flag ("1 ") still means deleted — the field is trimmed.
		{
			name:   "deleted padded DEL",
			line:   "A,B,:\x04\x04T\x04\x04\x041\x040\x041\x041 \x04fb2\x04\x04\x04",
			wantOK: false,
		},
		{name: "too few fields", line: "a\x04b\x04c", wantOK: false},
		{
			name:    "region tag normalized",
			line:    inpLine("Doe,John,:", "prose:", "Title", "", "", "5", "100", "0", "en-US"),
			wantOK:  true,
			title:   "Title",
			authors: []string{"John Doe"},
			genres:  []string{"prose"},
			lang:    "en",
			path:    "arc.zip/5.fb2",
		},
		{name: "blank", line: "", wantOK: false},
	}

	for i := range tests {
		tt := tests[i]
		s.Run(tt.name, func() {
			rec, ok := parseINPLine(tt.line, 7, "arc.zip")
			s.Require().Equal(tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			s.Equal(int64(7), rec.LibraryID)
			s.Equal(tt.title, rec.Title)
			s.Equal(tt.authors, rec.Authors)
			s.Equal(tt.genres, rec.Genres)
			s.Equal(tt.series, rec.Series)
			s.Equal(tt.sernoOK, rec.SeriesNumber.Valid)
			if tt.sernoOK {
				s.InDelta(tt.serno, rec.SeriesNumber.Float64, 0.0001)
			}
			s.Equal(tt.lang, rec.Language)
			s.Equal(tt.path, rec.SourcePath)
		})
	}
}
