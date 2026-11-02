package db

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

type ftsSuite struct {
	suite.Suite

	db  *sql.DB
	q   *dbq.Queries
	lib int64
}

func (s *ftsSuite) SetupTest() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)

	s.lib, err = s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: "Main", Type: "folder", Path: "/lib", SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
}

func (s *ftsSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// book inserts a bare book row (no FTS row, no links).
func (s *ftsSuite) book(key, title string) int64 {
	id, err := s.q.InsertBook(context.Background(), dbq.InsertBookParams{
		LibraryID: s.lib, LibraryKey: key, Title: title, Language: "en",
		ContentHash: key, AddedAt: 1, ImportedAt: 1,
	})
	s.Require().NoError(err)

	return id
}

func (s *ftsSuite) author(bookID int64, name string) {
	ctx := context.Background()
	aid, err := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: name, NameFold: Fold(name)})
	s.Require().NoError(err)
	s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: bookID, AuthorID: aid}))
}

func (s *ftsSuite) exec(query string, args ...any) {
	_, err := s.db.ExecContext(context.Background(), query, args...)
	s.Require().NoError(err)
}

// match returns the rowids matching an FTS5 query.
func (s *ftsSuite) match(query string) []int64 {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT rowid FROM books_fts WHERE books_fts MATCH ? ORDER BY rowid`, query)
	s.Require().NoError(err)
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		s.Require().NoError(rows.Scan(&id))
		ids = append(ids, id)
	}
	s.Require().NoError(rows.Err())

	return ids
}

func (s *ftsSuite) TestRefreshInsertsMissingRow() {
	ctx := context.Background()
	id := s.book("k1", "Dune")
	s.author(id, "Frank")
	s.author(id, "Herbert")
	s.exec(`UPDATE books SET annotation = ? WHERE id = ?`, "<p>Desert <b>planet</b></p>", id)

	s.Empty(s.match("dune"))
	s.Require().NoError(RefreshBookFTS(ctx, s.db, id))

	s.Equal([]int64{id}, s.match("title:dune"))
	s.Equal([]int64{id}, s.match("authors:herbert"))
	s.Equal([]int64{id}, s.match("annotation:planet"))
	s.Empty(s.match("annotation:b"), "markup must be stripped")
	// Link order: Frank was linked before Herbert.
	s.Equal([]int64{id}, s.match(`authors:"frank herbert"`))
}

func (s *ftsSuite) TestRefreshReplacesChangedTerms() {
	ctx := context.Background()
	id := s.book("k1", "Dune")
	s.author(id, "Herbert")
	s.Require().NoError(RefreshBookFTS(ctx, s.db, id))
	s.Equal([]int64{id}, s.match("dune"))

	sid, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: "Arrakis", NameFold: Fold("Arrakis")})
	s.Require().NoError(err)
	s.exec(`UPDATE books SET title = 'Messiah', series_id = ?, annotation = 'sandworm' WHERE id = ?`, sid, id)
	s.exec(`DELETE FROM book_authors WHERE book_id = ?`, id)
	s.author(id, "Brian")

	s.Require().NoError(RefreshBookFTS(ctx, s.db, id))

	s.Equal([]int64{id}, s.match("title:messiah"))
	s.Empty(s.match("title:dune"))
	s.Equal([]int64{id}, s.match("authors:brian"))
	s.Empty(s.match("authors:herbert"))
	s.Equal([]int64{id}, s.match("series:arrakis"))
	s.Equal([]int64{id}, s.match("annotation:sandworm"))

	// A second refresh with nothing changed must not duplicate or lose the row.
	s.Require().NoError(RefreshBookFTS(ctx, s.db, id))
	s.Equal([]int64{id}, s.match("messiah"))
}

func (s *ftsSuite) TestRefreshMissingBookErrors() {
	s.Error(RefreshBookFTS(context.Background(), s.db, 9999))
}
