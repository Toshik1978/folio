package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"
)

// ftsMigrationSuite covers the 005 rebuild against a database that already holds
// data. Every other suite opens a fresh database, where goose runs 001..005 in
// one pass and the rebuild's INSERT..SELECT copies nothing — so without this the
// only migration that has to move 573k rows in production would be exercised
// exclusively against an empty table.
type ftsMigrationSuite struct {
	suite.Suite

	db *sql.DB
}

// migrationVersions are the goose versions bracketing the FTS rebuild.
const (
	versionBeforeFTSRowid = 4
	versionFTSRowid       = 5
)

// SetupTest opens a database migrated only as far as 004, i.e. with books_fts
// still keyed by the TEXT book_id column.
func (s *ftsMigrationSuite) SetupTest() {
	database, err := sql.Open("sqlite", filepath.Join(s.T().TempDir(), "folio.db")+"?"+pragmaDSN)
	s.Require().NoError(err)
	s.db = database

	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	s.Require().NoError(goose.SetDialect("sqlite3"))
	s.Require().NoError(goose.UpTo(database, "migrations", versionBeforeFTSRowid))

	_, err = database.ExecContext(context.Background(),
		`INSERT INTO libraries (name, type, path, sync_interval_seconds, created_at, status)
		 VALUES ('L', 'inpx', '/lib', 3600, 0, 'active')`)
	s.Require().NoError(err)
}

func (s *ftsMigrationSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// seedPreRowid inserts a book and its old-style FTS row, where book_id is the
// TEXT rendering of the book id and the rowid is whatever FTS5 assigned.
func (s *ftsMigrationSuite) seedPreRowid(id int64, title, authors string) {
	ctx := context.Background()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO books (id, library_id, library_key, title, language, content_hash, added_at, imported_at)
		 VALUES (?, 1, ?, ?, 'ru', ?, 0, 0)`,
		id, "key-"+title, title, "hash-"+title)
	s.Require().NoError(err)

	// The pre-005 write path: book_id bound as TEXT, rowid left to FTS5.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO books_fts (book_id, title, authors, series, annotation) VALUES (?, ?, ?, '', '')`,
		strconv.FormatInt(id, 10), title, authors)
	s.Require().NoError(err)
}

// TestRebuildPreservesRowsAndRekeysByRowid is the guard on the one-time data
// migration: existing rows must survive it, keyed by books.id.
func (s *ftsMigrationSuite) TestRebuildPreservesRowsAndRekeysByRowid() {
	ctx := context.Background()

	// Seed out of insertion order so a rebuild that leaned on FTS5's own rowid
	// sequence rather than book_id would mis-key the rows.
	s.seedPreRowid(42, "Alpha", "Ivan Petrov")
	s.seedPreRowid(7, "Beta", "Anna Sidorova")

	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSRowid))

	for _, tc := range []struct {
		id    int64
		title string
	}{{42, "Alpha"}, {7, "Beta"}} {
		var title string
		err := s.db.QueryRowContext(ctx, `SELECT title FROM books_fts WHERE rowid = ?`, tc.id).Scan(&title)
		s.Require().NoError(err, "book %d must be reachable by rowid after the rebuild", tc.id)
		s.Equal(tc.title, title)
	}

	// The index itself must still be searchable, not merely present.
	var n int
	s.Require().NoError(
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM books_fts WHERE books_fts MATCH ?`, "Petrov").Scan(&n))
	s.Equal(1, n, "tokens must survive the rebuild")
}

// TestRebuiltTriggerPurgesDeletedBook guards the trigger recreated by 005: the
// pre-005 form matched book_id against old.id and worked only through SQLite's
// TEXT/INTEGER affinity coercion, so the rowid form needs its own coverage.
func (s *ftsMigrationSuite) TestRebuiltTriggerPurgesDeletedBook() {
	ctx := context.Background()

	s.seedPreRowid(42, "Alpha", "Ivan Petrov")
	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSRowid))

	_, err := s.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, 42)
	s.Require().NoError(err)

	var n int
	s.Require().NoError(s.db.QueryRowContext(ctx, `SELECT count(*) FROM books_fts`).Scan(&n))
	s.Zero(n, "books_ad must purge the FTS row by rowid")
}

// TestDownRestoresBookIDKey checks the Down leg, so a rollback off a bad release
// leaves an index the previous binary can still write to.
func (s *ftsMigrationSuite) TestDownRestoresBookIDKey() {
	ctx := context.Background()

	s.seedPreRowid(42, "Alpha", "Ivan Petrov")
	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSRowid))
	s.Require().NoError(goose.DownTo(s.db, "migrations", versionBeforeFTSRowid))

	var title string
	err := s.db.QueryRowContext(ctx, `SELECT title FROM books_fts WHERE book_id = ?`, "42").Scan(&title)
	s.Require().NoError(err, "book_id column must be restored and populated")
	s.Equal("Alpha", title)

	// The restored trigger relies on the same affinity coercion as the original.
	_, err = s.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, 42)
	s.Require().NoError(err)

	var n int
	s.Require().NoError(s.db.QueryRowContext(ctx, `SELECT count(*) FROM books_fts`).Scan(&n))
	s.Zero(n, "restored books_ad must still purge by book_id")
}
