package db

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type deepIndexWorkSuite struct {
	suite.Suite

	db  *sql.DB
	q   *dbq.Queries
	lib int64
}

func (s *deepIndexWorkSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)
	s.lib = s.newLibrary("Main", "/lib")
}

func (s *deepIndexWorkSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *deepIndexWorkSuite) newLibrary(name, path string) int64 {
	id, err := s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: name, Type: "folder", Path: path, SyncIntervalSeconds: 3600, CreatedAt: 1,
	})
	s.Require().NoError(err)
	return id
}

// book seeds a book with one file per (format, path) pair, in the given order.
func (s *deepIndexWorkSuite) book(lib int64, key string, importedAt int64, files ...[2]string) int64 {
	ctx := context.Background()
	id, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: lib, LibraryKey: key, Title: key, Language: "en",
		ContentHash: key, AddedAt: 1, ImportedAt: importedAt,
	})
	s.Require().NoError(err)
	for _, f := range files {
		_, err = s.q.InsertBookFile(ctx, dbq.InsertBookFileParams{
			BookID: id, FileFormat: f[0], FileSize: 1, SourcePath: f[1],
		})
		s.Require().NoError(err)
	}

	return id
}

func (s *deepIndexWorkSuite) work(lib int64, importedAt sql.NullInt64) []dbq.ListDeepIndexWorkRow {
	rows, err := s.q.ListDeepIndexWork(context.Background(), dbq.ListDeepIndexWorkParams{
		LibraryID: lib, ImportedAt: importedAt,
	})
	s.Require().NoError(err)
	return rows
}

func (s *deepIndexWorkSuite) TestPreferenceOrder() {
	cases := []struct {
		name  string
		files [][2]string
		want  string
	}{
		{
			"fb2 beats all",
			[][2]string{{"pdf", "a.pdf"}, {"mobi", "a.mobi"}, {"epub", "a.epub"}, {"fb2", "a.fb2"}},
			"fb2",
		},
		{"epub beats mobi and pdf", [][2]string{{"pdf", "b.pdf"}, {"mobi", "b.mobi"}, {"epub", "b.epub"}}, "epub"},
		{"mobi beats pdf", [][2]string{{"pdf", "c.pdf"}, {"mobi", "c.mobi"}}, "mobi"},
		{"other beats pdf", [][2]string{{"pdf", "d.pdf"}, {"azw3", "d.azw3"}}, "azw3"},
		{"pdf only", [][2]string{{"pdf", "e.pdf"}}, "pdf"},
	}
	for _, c := range cases {
		s.Run(c.name, func() {
			lib := s.newLibrary(c.name, "/"+c.name)
			id := s.book(lib, "k", 10, c.files...)
			rows := s.work(lib, sql.NullInt64{})
			s.Require().Len(rows, 1)
			s.Equal(id, rows[0].BookID)
			s.Equal(c.want, rows[0].FileFormat)
			s.NotZero(rows[0].FileID)
			s.NotEmpty(rows[0].SourcePath)
		})
	}
}

func (s *deepIndexWorkSuite) TestOneRowPerBookOrderedBySourcePath() {
	s.book(s.lib, "z", 10, [2]string{"epub", "z.epub"}, [2]string{"fb2", "z.fb2"})
	s.book(s.lib, "a", 10, [2]string{"epub", "a.epub"})
	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 2)
	s.Equal("a.epub", rows[0].SourcePath)
	s.Equal("z.fb2", rows[1].SourcePath)
}

func (s *deepIndexWorkSuite) TestSameFormatTieBreaksByFileID() {
	s.book(s.lib, "t", 10, [2]string{"epub", "second.epub"}, [2]string{"epub", "first.epub"})
	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 1)
	s.Equal("second.epub", rows[0].SourcePath)
}

func (s *deepIndexWorkSuite) TestCheckedBooksExcluded() {
	done := s.book(s.lib, "done", 10, [2]string{"fb2", "done.fb2"})
	s.book(s.lib, "todo", 10, [2]string{"fb2", "todo.fb2"})
	s.Require().NoError(s.q.MarkMetadataChecked(context.Background(), done))
	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 1)
	s.Equal("todo.fb2", rows[0].SourcePath)
}

func (s *deepIndexWorkSuite) TestImportedAtFilter() {
	s.book(s.lib, "old", 10, [2]string{"fb2", "old.fb2"})
	s.book(s.lib, "new", 20, [2]string{"fb2", "new.fb2"})
	rows := s.work(s.lib, sql.NullInt64{Int64: 20, Valid: true})
	s.Require().Len(rows, 1)
	s.Equal("new.fb2", rows[0].SourcePath)
	s.Len(s.work(s.lib, sql.NullInt64{}), 2)
}

func (s *deepIndexWorkSuite) TestLibraryScoped() {
	other := s.newLibrary("Other", "/other")
	s.book(other, "x", 10, [2]string{"fb2", "x.fb2"})
	s.Empty(s.work(s.lib, sql.NullInt64{}))
}
