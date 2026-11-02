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

// paths returns the source_path of every row, in order.
func paths(rows []dbq.ListDeepIndexWorkRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SourcePath
	}

	return out
}

func (s *deepIndexWorkSuite) TestEveryFileOfABookIsReturned() {
	id := s.book(s.lib, "k", 10,
		[2]string{"pdf", "a.pdf"}, [2]string{"mobi", "a.mobi"}, [2]string{"epub", "a.epub"}, [2]string{"fb2", "a.fb2"})
	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 4, "no format is preferred in SQL")
	formats := map[string]string{}
	for _, r := range rows {
		s.Equal(id, r.BookID)
		s.NotZero(r.FileID)
		formats[r.SourcePath] = r.FileFormat
	}
	s.Equal(map[string]string{"a.pdf": "pdf", "a.mobi": "mobi", "a.epub": "epub", "a.fb2": "fb2"}, formats)
}

func (s *deepIndexWorkSuite) TestRowsOrderedBySourcePath() {
	s.book(s.lib, "z", 10, [2]string{"epub", "z.epub"}, [2]string{"fb2", "c.fb2"})
	s.book(s.lib, "a", 10, [2]string{"epub", "a.epub"})
	rows := s.work(s.lib, sql.NullInt64{})
	s.Equal([]string{"a.epub", "c.fb2", "z.epub"}, paths(rows))
}

func (s *deepIndexWorkSuite) TestSameSourcePathTieBreaksByBookID() {
	first := s.book(s.lib, "first", 10, [2]string{"fb2", "same.fb2"})
	second := s.book(s.lib, "second", 10, [2]string{"fb2", "same.fb2"})
	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 2)
	s.Equal(first, rows[0].BookID)
	s.Equal(second, rows[1].BookID)
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
