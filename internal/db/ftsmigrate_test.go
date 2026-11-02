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
	versionFTSContentless = 6
	versionFTSVocabCol    = 8
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
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM books_fts WHERE books_fts MATCH ?`, "Petrov").Scan(&n),
	)
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

// seedRowidKeyed inserts a book and its post-005 FTS row (keyed by rowid), for
// exercising the 006 contentless rebuild.
func (s *ftsMigrationSuite) seedRowidKeyed(id int64, title, authors, annotation string) {
	ctx := context.Background()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO books (id, library_id, library_key, title, language, content_hash, added_at, imported_at)
		 VALUES (?, 1, ?, ?, 'ru', ?, 0, 0)`,
		id, "key-"+title, title, "hash-"+title)
	s.Require().NoError(err)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO books_fts (rowid, title, authors, series, annotation) VALUES (?, ?, ?, '', ?)`,
		id, title, authors, annotation)
	s.Require().NoError(err)
}

func (s *ftsMigrationSuite) migrateToContentless() {
	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSRowid))
	s.seedRowidKeyed(42, "Alpha", "Ivan Petrov", "A tale of zeppelins")
	s.seedRowidKeyed(7, "Beta", "Anna Sidorova", "Zeppelins again")
	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSContentless))
}

func (s *ftsMigrationSuite) count(query string, args ...any) int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(context.Background(), query, args...).Scan(&n))

	return n
}

// TestContentlessPreservesRowsAndSearch checks the copy keeps every row, keyed
// by rowid, and that title, author and annotation terms are still found.
func (s *ftsMigrationSuite) TestContentlessPreservesRowsAndSearch() {
	s.migrateToContentless()

	s.Equal(2, s.count(`SELECT count(*) FROM books_fts`))
	for _, term := range []string{"Alpha", "Petrov", "zeppelins"} {
		s.Positive(s.count(`SELECT count(*) FROM books_fts WHERE books_fts MATCH ?`, term), term)
	}
	s.Equal(42, s.count(`SELECT rowid FROM books_fts WHERE books_fts MATCH 'Petrov'`))
}

func (s *ftsMigrationSuite) TestContentlessDeleteRemovesFTSRow() {
	s.migrateToContentless()

	_, err := s.db.ExecContext(context.Background(), `DELETE FROM books WHERE id = 42`)
	s.Require().NoError(err)

	s.Equal(1, s.count(`SELECT count(*) FROM books_fts`))
	s.Zero(s.count(`SELECT count(*) FROM books_fts WHERE books_fts MATCH 'Petrov'`))
}

func (s *ftsMigrationSuite) TestContentlessHasNoContentTable() {
	s.migrateToContentless()

	s.Zero(s.count(`SELECT count(*) FROM sqlite_master WHERE name = 'books_fts_content'`))
}

func (s *ftsMigrationSuite) TestVocabReturnsDocumentFrequency() {
	s.migrateToContentless()

	s.Equal(2, s.count(`SELECT doc FROM books_fts_vocab WHERE term = 'zeppelins'`))
	s.Equal(1, s.count(`SELECT doc FROM books_fts_vocab WHERE term = 'petrov'`))
}

// TestVocabColCountsPerColumn checks migration 008 swaps the row vocab view for
// a per-column one, so a description's document frequency ignores titles and
// authors, and that its Down leg restores the row view.
func (s *ftsMigrationSuite) TestVocabColCountsPerColumn() {
	s.migrateToContentless()
	s.Require().NoError(goose.UpTo(s.db, "migrations", versionFTSVocabCol))

	s.Equal(2, s.count(`SELECT doc FROM books_fts_vocab_col WHERE term = 'zeppelins' AND col = 'annotation'`))
	s.Equal(1, s.count(`SELECT doc FROM books_fts_vocab_col WHERE term = 'alpha' AND col = 'title'`))
	s.Zero(s.count(`SELECT count(*) FROM books_fts_vocab_col WHERE term = 'alpha' AND col = 'annotation'`))
	s.Zero(s.count(`SELECT count(*) FROM sqlite_master WHERE name = 'books_fts_vocab'`))

	s.Require().NoError(goose.DownTo(s.db, "migrations", versionFTSContentless))
	s.Equal(2, s.count(`SELECT doc FROM books_fts_vocab WHERE term = 'zeppelins'`))
	s.Zero(s.count(`SELECT count(*) FROM sqlite_master WHERE name = 'books_fts_vocab_col'`))
}

// TestContentlessDownRebuildsFromBooks checks the Down leg restores a content
// table from the books joins, dropping the vocab table.
func (s *ftsMigrationSuite) TestContentlessDownRebuildsFromBooks() {
	ctx := context.Background()
	s.migrateToContentless()

	for _, q := range []string{
		`INSERT INTO authors (id, name, name_fold) VALUES (1, 'Ivan Petrov', 'IVAN PETROV'), (2, 'Co Author', 'CO AUTHOR')`,
		`INSERT INTO book_authors (book_id, author_id) VALUES (42, 1), (42, 2)`,
		`INSERT INTO series (id, name, name_fold) VALUES (1, 'Saga', 'SAGA')`,
		`UPDATE books SET series_id = 1, annotation = '<p>A tale</p>' WHERE id = 42`,
	} {
		_, err := s.db.ExecContext(ctx, q)
		s.Require().NoError(err)
	}

	s.Require().NoError(goose.DownTo(s.db, "migrations", versionFTSRowid))

	s.Equal(2, s.count(`SELECT count(*) FROM books_fts`))
	s.Zero(s.count(`SELECT count(*) FROM sqlite_master WHERE name = 'books_fts_vocab'`))

	var authors, series, annotation string
	s.Require().NoError(s.db.QueryRowContext(ctx,
		`SELECT authors, series, annotation FROM books_fts WHERE rowid = 42`).Scan(&authors, &series, &annotation))
	s.Equal("Ivan Petrov Co Author", authors)
	s.Equal("Saga", series)
	s.Equal("<p>A tale</p>", annotation)

	_, err := s.db.ExecContext(ctx, `DELETE FROM books WHERE id = 42`)
	s.Require().NoError(err)
	s.Equal(1, s.count(`SELECT count(*) FROM books_fts`))
}
