package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/stephenafamo/bob/dialect/sqlite"
	"github.com/stephenafamo/bob/dialect/sqlite/im"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/htmltext"
)

// books_fts writes live here rather than in sqlc because sqlc cannot generate
// them: the table is keyed by rowid (= books.id, see migration 005), and FTS5
// reserves "rowid" as a column name, so no such column exists in the schema sqlc
// parses. It refuses every statement referencing it. The rowid key is not
// optional — FTS5 can seek only by rowid or MATCH, so a predicate on any real
// column scans the entire index, which is what made a single book delete cost
// 335ms on a 573k-book catalog.
//
// These follow the same escape hatch booksfilter.go uses for the faceted
// filter: bob builds the statement with every value parameterized, and
// database/sql executes it. Callers pass the same handle their dbq.Queries was
// built from, so an FTS write joins the caller's transaction.

// BookFTSRow is one book's full-text projection, written when the book is first
// inserted. Fields carry the already-normalized values (authors joined, markup
// stripped) — this layer does no formatting of its own.
type BookFTSRow struct {
	BookID     int64
	Title      string
	Authors    string
	Series     string
	Annotation string
}

// InsertBookFTS adds a book's full-text row, keyed by rowid so later updates and
// the books_ad delete trigger can seek straight to it.
func InsertBookFTS(ctx context.Context, x dbq.DBTX, row BookFTSRow) error {
	query, args, err := sqlite.Insert(
		im.Into("books_fts", "rowid", "title", "authors", "series", "annotation"),
		im.Values(
			sqlite.Arg(row.BookID), sqlite.Arg(row.Title), sqlite.Arg(row.Authors),
			sqlite.Arg(row.Series), sqlite.Arg(row.Annotation),
		),
	).Build(ctx)
	if err != nil {
		return fmt.Errorf("build fts insert: %w", err)
	}
	if _, err := x.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("exec fts insert: %w", err)
	}

	return nil
}

// RefreshBookFTS recomputes a book's whole full-text row from books, its author
// links, its series and its annotation, and writes it: a full-row UPDATE when the
// row exists, an INSERT when it does not.
//
// books_fts is contentless, so a partial-column UPDATE is rejected and column
// values cannot be read back; callers change books/joins first, then call this
// inside the same transaction. Existence is probed by rowid (a seek, which
// contentless tables support) rather than DELETE+INSERT, so the common refresh is
// one in-place rewrite and the books_ad delete trigger is never involved.
func RefreshBookFTS(ctx context.Context, x dbq.DBTX, bookID int64) error {
	row := BookFTSRow{BookID: bookID}
	var annotation sql.NullString
	err := x.QueryRowContext(ctx, gatherBookFTSSQL, bookID).Scan(
		&row.Title, &row.Authors, &row.Series, &annotation,
	)
	if err != nil {
		return fmt.Errorf("gather fts row for book %d: %w", bookID, err)
	}
	row.Annotation = htmltext.StripMarkup(annotation.String)

	var one int
	err = x.QueryRowContext(ctx, `SELECT 1 FROM books_fts WHERE rowid = ?`, bookID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return InsertBookFTS(ctx, x, row)
	case err != nil:
		return fmt.Errorf("probe fts row: %w", err)
	}

	_, err = x.ExecContext(ctx,
		`UPDATE books_fts SET title = ?, authors = ?, series = ?, annotation = ? WHERE rowid = ?`,
		row.Title, row.Authors, row.Series, row.Annotation, bookID)
	if err != nil {
		return fmt.Errorf("exec fts update: %w", err)
	}

	return nil
}

// gatherBookFTSSQL reads the four indexed values. book_authors has no position
// column, so link order is its rowid order.
const gatherBookFTSSQL = `
SELECT b.title,
       COALESCE((SELECT group_concat(name, ' ')
                 FROM (SELECT a.name
                       FROM book_authors ba
                                JOIN authors a ON a.id = ba.author_id
                       WHERE ba.book_id = b.id
                       ORDER BY ba.rowid)), ''),
       COALESCE(s.name, ''),
       b.annotation
FROM books b
         LEFT JOIN series s ON s.id = b.series_id
WHERE b.id = ?`
