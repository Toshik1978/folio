package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// connSuite guards the connection-level pragmas carried in the DSN (see
// pragmaDSN). foreign_keys and busy_timeout are per-connection and not persisted,
// so a regression that moves them back to a one-off db.Exec would silently leave
// most of the pool with foreign_keys=0 (ON DELETE CASCADE skipped) and
// busy_timeout=0 (immediate SQLITE_BUSY on contention).
type connSuite struct {
	suite.Suite

	db *sql.DB
}

func (s *connSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
}

func (s *connSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// TestPragmasHoldOnEveryConnection holds several pool connections open at once and
// asserts each one — not just the first — has foreign_keys and busy_timeout set.
func (s *connSuite) TestPragmasHoldOnEveryConnection() {
	ctx := context.Background()
	s.db.SetMaxOpenConns(4)

	conns := make([]*sql.Conn, 0, 4)
	for range 4 {
		c, err := s.db.Conn(ctx)
		s.Require().NoError(err)
		conns = append(conns, c)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for i, c := range conns {
		var fk, bt int
		s.Require().NoError(c.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&fk))
		s.Require().NoError(c.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&bt))
		s.Equalf(1, fk, "foreign_keys must be on for connection %d", i)
		s.Equalf(5000, bt, "busy_timeout must be set for connection %d", i)
	}
}

// TestConnectionPoolIsBounded guards the explicit pool cap in Open. An uncapped
// pool (database/sql's default) would let read concurrency grow the number of
// open SQLite handles without limit on the low-spec target hosts.
func (s *connSuite) TestConnectionPoolIsBounded() {
	s.Equal(maxOpenConns, s.db.Stats().MaxOpenConnections)
}

// TestForeignKeyCascadeDeletesChildren confirms ON DELETE CASCADE actually fires:
// deleting a book removes its book_files and book_authors rows. This is the
// behaviour that breaks when foreign_keys is off on the serving connection.
func (s *connSuite) TestForeignKeyCascadeDeletesChildren() {
	ctx := context.Background()
	q := dbq.New(s.db)

	libID, err := q.InsertLibrary(ctx, dbq.InsertLibraryParams{
		Name: "Main", Type: "folder", Path: "/lib", SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)

	bookID, err := q.InsertBook(ctx, dbq.InsertBookParams{
		LibraryID: libID, LibraryKey: "k", Title: "T", Language: "en",
		ContentHash: "h", AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)

	_, err = q.InsertBookFile(ctx, dbq.InsertBookFileParams{
		BookID: bookID, FileFormat: "epub", FileSize: 1, SourcePath: "a.epub",
	})
	s.Require().NoError(err)

	authorID, err := q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: "Some Author", NameFold: Fold("Some Author")})
	s.Require().NoError(err)
	s.Require().NoError(q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: bookID, AuthorID: authorID}))

	s.Require().NoError(q.DeleteBook(ctx, bookID))

	s.Equal(0, s.count("SELECT COUNT(*) FROM book_files WHERE book_id = ?", bookID))
	s.Equal(0, s.count("SELECT COUNT(*) FROM book_authors WHERE book_id = ?", bookID))
}

func (s *connSuite) count(query string, args ...any) int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

// TestSloggerFatalf asserts the goose Fatalf adapter terminates (per goose's
// contract a failed migration must not continue into serving). The os.Exit seam
// is overridden so the assertion runs without killing the test process.
func (s *connSuite) TestSloggerFatalf() {
	var code int
	sl := newSlogger(slog.New(slog.DiscardHandler), func(c int) { code = c })
	sl.Fatalf("fatal test error: %s", "something bad")
	s.Equal(1, code, "Fatalf must terminate with a non-zero exit code")
}

// FoldNull pairs with Fold: it feeds books.publisher_fold, which must always
// agree with the publisher column (NULL/blank folds to NULL).
func (s *connSuite) TestFoldNull() {
	s.False(FoldNull(sql.NullString{}).Valid, "NULL publisher folds to NULL")
	s.False(FoldNull(sql.NullString{String: "   ", Valid: true}).Valid, "blank publisher folds to NULL")
	s.Equal(
		sql.NullString{String: "ИЗДАТЕЛЬСТВО", Valid: true},
		FoldNull(sql.NullString{String: "издательство", Valid: true}),
	)
}

// TestIsUniqueViolation confirms the helper distinguishes a UNIQUE constraint
// failure (duplicate library path) from an unrelated error, so the API can map
// it to 409 rather than 500.
func (s *connSuite) TestIsUniqueViolation() {
	ctx := context.Background()
	q := dbq.New(s.db)
	_, err := q.InsertLibrary(ctx, dbq.InsertLibraryParams{
		Name: "A", Type: "folder", Path: "/dup", SyncIntervalSeconds: 3600, CreatedAt: 1,
	})
	s.Require().NoError(err)

	_, err = q.InsertLibrary(ctx, dbq.InsertLibraryParams{
		Name: "B", Type: "folder", Path: "/dup", SyncIntervalSeconds: 3600, CreatedAt: 2,
	})
	s.Require().Error(err)
	s.True(IsUniqueViolation(err), "duplicate path is a UNIQUE violation")
	s.False(IsUniqueViolation(errors.New("some other error")))
}

// TestFoldDiacritics guards the M6 fix: Ё folds to Е and Latin diacritics are
// stripped (matching FTS remove_diacritics 1), while non-Latin marks survive, so
// these names file under a real letter bucket instead of '#'.
func (s *connSuite) TestFoldDiacritics() {
	s.Equal("ЕЛКИН", Fold("Ёлкин"), "Ё files next to Е, not '#'")
	s.Equal("ЕЛКИН", Fold("ёлкин"), "lowercase ё too")
	s.Equal("EMILE ZOLA", Fold("Émile Zola"), "Latin diacritics stripped (FTS parity)")
	s.Equal("ЙЕМЕН", Fold("Йемен"), "Cyrillic Й is a distinct letter — must survive")
	s.Equal("PENGUIN", Fold("penguin"), "plain ASCII unchanged")
	s.Equal("ИЗДАТЕЛЬСТВО", Fold("издательство"), "Cyrillic uppercasing unchanged")
}
