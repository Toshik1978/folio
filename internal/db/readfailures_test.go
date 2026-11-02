package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

const versionReadFailures = 7

// readFailuresSuite covers the book_read_failures table: migration round trip,
// CHECK constraints, upsert semantics and the ON DELETE CASCADE from books.
type readFailuresSuite struct {
	suite.Suite

	db *sql.DB
}

func (s *readFailuresSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
}

func (s *readFailuresSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *readFailuresSuite) newBook(libID int64, key string) int64 {
	id, err := dbq.New(s.db).InsertBook(context.Background(), dbq.InsertBookParams{
		LibraryID: libID, LibraryKey: key, Title: "T", Language: "en", ContentHash: "h", AddedAt: 1,
	})
	s.Require().NoError(err)
	return id
}

func (s *readFailuresSuite) newLibrary(path string) int64 {
	id, err := dbq.New(s.db).InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: "L", Type: "folder", Path: path, SyncIntervalSeconds: 3600, CreatedAt: 1,
	})
	s.Require().NoError(err)
	return id
}

func (s *readFailuresSuite) record(bookID int64, flow, reason string) error {
	err := dbq.New(s.db).UpsertReadFailure(context.Background(), dbq.UpsertReadFailureParams{
		BookID: bookID, Flow: flow, Reason: reason, Detail: "d", Fingerprint: "fp", ReaderVersion: 1, RecordedAt: 10,
	})
	if err != nil {
		return fmt.Errorf("upsert read failure: %w", err)
	}

	return nil
}

func (s *readFailuresSuite) count(query string, args ...any) int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

func (s *readFailuresSuite) tableExists() bool {
	return s.count("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'book_read_failures'") == 1
}

// TestMigrationRoundTrip applies 007 down and back up on a database file.
func (s *readFailuresSuite) TestMigrationRoundTrip() {
	database, err := sql.Open("sqlite", filepath.Join(s.T().TempDir(), "folio.db")+"?"+pragmaDSN)
	s.Require().NoError(err)
	defer func() { _ = database.Close() }()

	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	s.Require().NoError(goose.SetDialect("sqlite3"))

	s.Require().NoError(goose.UpTo(database, "migrations", versionReadFailures))
	exists := func() bool {
		var n int
		s.Require().NoError(database.QueryRowContext(
			context.Background(),
			"SELECT COUNT(*) FROM sqlite_master WHERE name IN ('book_read_failures', 'idx_book_read_failures_flow_version')",
		).
			Scan(&n))

		return n == 2
	}
	s.True(exists())

	s.Require().NoError(goose.DownTo(database, "migrations", versionReadFailures-1))
	s.False(exists())

	s.Require().NoError(goose.UpTo(database, "migrations", versionReadFailures))
	s.True(exists())
}

func (s *readFailuresSuite) TestOpenCreatesTable() {
	s.True(s.tableExists())
}

func (s *readFailuresSuite) TestCheckConstraintsRejectUnknownValues() {
	bookID := s.newBook(s.newLibrary("/lib"), "k")

	s.Require().Error(s.record(bookID, "thumbnail", "corrupt"), "unknown flow")
	s.Require().Error(s.record(bookID, "metadata", "bogus"), "unknown reason")
	s.Zero(s.count("SELECT COUNT(*) FROM book_read_failures"))

	for _, reason := range []string{"corrupt", "missing_entry", "too_large", "crashed"} {
		s.Require().NoError(s.record(bookID, "cover", reason), reason)
	}
	s.Require().NoError(s.record(bookID, "metadata", "corrupt"))
}

func (s *readFailuresSuite) TestUpsertReplacesRow() {
	ctx := context.Background()
	q := dbq.New(s.db)
	bookID := s.newBook(s.newLibrary("/lib"), "k")

	s.Require().NoError(s.record(bookID, "metadata", "corrupt"))
	s.Require().NoError(q.UpsertReadFailure(ctx, dbq.UpsertReadFailureParams{
		BookID:        bookID,
		Flow:          "metadata",
		Reason:        "crashed",
		Detail:        "boom",
		Fingerprint:   "fp2",
		ReaderVersion: 2,
		RecordedAt:    20,
	}))

	s.Equal(1, s.count("SELECT COUNT(*) FROM book_read_failures"))
	got, err := q.GetReadFailure(ctx, dbq.GetReadFailureParams{BookID: bookID, Flow: "metadata"})
	s.Require().NoError(err)
	s.Equal(dbq.BookReadFailure{
		BookID: bookID, Flow: "metadata", Reason: "crashed", Detail: "boom",
		Fingerprint: "fp2", ReaderVersion: 2, RecordedAt: 20,
	}, got)
}

func (s *readFailuresSuite) TestDeleteQueries() {
	ctx := context.Background()
	q := dbq.New(s.db)
	bookID := s.newBook(s.newLibrary("/lib"), "k")
	s.Require().NoError(s.record(bookID, "metadata", "corrupt"))
	s.Require().NoError(s.record(bookID, "cover", "corrupt"))

	s.Require().NoError(q.DeleteReadFailure(ctx, dbq.DeleteReadFailureParams{BookID: bookID, Flow: "cover"}))
	s.Equal(1, s.count("SELECT COUNT(*) FROM book_read_failures WHERE book_id = ?", bookID))
	_, err := q.GetReadFailure(ctx, dbq.GetReadFailureParams{BookID: bookID, Flow: "cover"})
	s.Require().ErrorIs(err, sql.ErrNoRows)

	s.Require().NoError(q.DeleteReadFailuresForBook(ctx, bookID))
	s.Zero(s.count("SELECT COUNT(*) FROM book_read_failures"))
}

func (s *readFailuresSuite) TestDeletingBookCascades() {
	bookID := s.newBook(s.newLibrary("/lib"), "k")
	s.Require().NoError(s.record(bookID, "metadata", "corrupt"))

	s.Require().NoError(dbq.New(s.db).DeleteBook(context.Background(), bookID))
	s.Zero(s.count("SELECT COUNT(*) FROM book_read_failures"))
}

// TestPurgingLibraryCascades runs the same statements the sync engine's purge
// issues (DeleteBooksByLibrary then DeleteLibrary, in one transaction).
func (s *readFailuresSuite) TestPurgingLibraryCascades() {
	ctx := context.Background()
	libID := s.newLibrary("/purged")
	keepLib := s.newLibrary("/kept")
	gone := s.newBook(libID, "a")
	kept := s.newBook(keepLib, "b")
	s.Require().NoError(s.record(gone, "metadata", "corrupt"))
	s.Require().NoError(s.record(gone, "cover", "too_large"))
	s.Require().NoError(s.record(kept, "metadata", "corrupt"))

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	tq := dbq.New(tx)
	s.Require().NoError(tq.DeleteBooksByLibrary(ctx, libID))
	s.Require().NoError(tq.DeleteLibrary(ctx, libID))
	s.Require().NoError(tx.Commit())

	s.Zero(s.count("SELECT COUNT(*) FROM book_read_failures WHERE book_id = ?", gone))
	s.Equal(1, s.count("SELECT COUNT(*) FROM book_read_failures WHERE book_id = ?", kept))
}
