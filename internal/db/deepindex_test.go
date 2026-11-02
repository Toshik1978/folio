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

// work returns the whole work list of lib in one page.
func (s *deepIndexWorkSuite) work(lib int64, importedAt sql.NullInt64) []dbq.ListDeepIndexWorkRow {
	return s.page(lib, importedAt, 0, 1000)
}

// page returns the work rows of up to lim books with an id above after.
func (s *deepIndexWorkSuite) page(lib int64, importedAt sql.NullInt64, after, lim int64) []dbq.ListDeepIndexWorkRow {
	rows, err := s.q.ListDeepIndexWork(context.Background(), dbq.ListDeepIndexWorkParams{
		LibraryID: lib, ImportedAt: importedAt, AfterID: after, Lim: lim,
	})
	s.Require().NoError(err)
	return rows
}

// count returns CountDeepIndexWork for lib.
func (s *deepIndexWorkSuite) count(lib int64, importedAt sql.NullInt64) int64 {
	n, err := s.q.CountDeepIndexWork(context.Background(), dbq.CountDeepIndexWorkParams{
		LibraryID: lib, ImportedAt: importedAt,
	})
	s.Require().NoError(err)
	return n
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

// recordFailure stores a broken-read record of the book for flow.
func (s *deepIndexWorkSuite) recordFailure(bookID int64, flow, fingerprint string, version int64) {
	err := s.q.UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: bookID, Flow: flow, Reason: "corrupt", Fingerprint: fingerprint,
		ReaderVersion: version, RecordedAt: 1,
	})
	s.Require().NoError(err)
}

func (s *deepIndexWorkSuite) TestMetadataFailureRecordIsJoined() {
	broken := s.book(s.lib, "broken", 10, [2]string{"fb2", "broken.fb2"}, [2]string{"epub", "broken.epub"})
	coverOnly := s.book(s.lib, "cover", 10, [2]string{"fb2", "cover.fb2"})
	clean := s.book(s.lib, "clean", 10, [2]string{"fb2", "clean.fb2"})
	s.recordFailure(broken, "metadata", "fp-meta", 3)
	s.recordFailure(broken, "cover", "fp-cover", 4)
	s.recordFailure(coverOnly, "cover", "fp-cover", 4)

	rows := s.work(s.lib, sql.NullInt64{})
	s.Require().Len(rows, 4, "the join adds no rows")
	for _, r := range rows {
		switch r.BookID {
		case broken:
			s.Equal(sql.NullString{String: "fp-meta", Valid: true}, r.FailureFingerprint, r.SourcePath)
			s.Equal(sql.NullInt64{Int64: 3, Valid: true}, r.FailureReaderVersion, r.SourcePath)
		case coverOnly, clean:
			s.False(r.FailureFingerprint.Valid, "a cover record is not a metadata record")
			s.False(r.FailureReaderVersion.Valid, r.SourcePath)
		}
	}
}

// TestPagesSplitByBook pages two books at a time: each page holds every file
// of its books, a page starts after the previous page's largest book id, and
// the pages together cover every book once.
func (s *deepIndexWorkSuite) TestPagesSplitByBook() {
	a := s.book(s.lib, "a", 10, [2]string{"fb2", "z.zip/a.fb2"}, [2]string{"epub", "a.epub"})
	b := s.book(s.lib, "b", 10, [2]string{"fb2", "b.fb2"})
	c := s.book(s.lib, "c", 10, [2]string{"fb2", "a.zip/c.fb2"}, [2]string{"mobi", "c.mobi"})

	first := s.page(s.lib, sql.NullInt64{}, 0, 2)
	s.Equal([]string{"a.epub", "b.fb2", "z.zip/a.fb2"}, paths(first), "both books' files, in source_path order")
	s.ElementsMatch([]int64{a, a, b}, []int64{first[0].BookID, first[1].BookID, first[2].BookID})

	second := s.page(s.lib, sql.NullInt64{}, b, 2)
	s.Equal([]string{"a.zip/c.fb2", "c.mobi"}, paths(second))
	s.Equal(c, second[0].BookID)

	s.Empty(s.page(s.lib, sql.NullInt64{}, c, 2))
}

// TestCountMatchesTheBooksPaged counts what the pages return: unchecked books
// with a file, in scope.
func (s *deepIndexWorkSuite) TestCountMatchesTheBooksPaged() {
	s.book(s.lib, "old", 10, [2]string{"fb2", "old.fb2"})
	s.book(s.lib, "new", 20, [2]string{"fb2", "new.fb2"}, [2]string{"epub", "new.epub"})
	done := s.book(s.lib, "done", 20, [2]string{"fb2", "done.fb2"})
	s.Require().NoError(s.q.MarkMetadataChecked(context.Background(), done))
	s.book(s.lib, "fileless", 20)
	other := s.newLibrary("Other", "/other")
	s.book(other, "x", 20, [2]string{"fb2", "x.fb2"})

	s.Equal(int64(2), s.count(s.lib, sql.NullInt64{}))
	s.Equal(int64(1), s.count(s.lib, sql.NullInt64{Int64: 20, Valid: true}))
	s.Len(s.work(s.lib, sql.NullInt64{}), 3, "the fileless book has no row either")
}
