package db

import (
	"context"
	"fmt"

	"github.com/stephenafamo/bob/dialect/sqlite"
	"github.com/stephenafamo/bob/dialect/sqlite/im"
	"github.com/stephenafamo/bob/dialect/sqlite/um"

	"github.com/Toshik1978/folio/internal/db/dbq"
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

// UpdateBookFTSTitle mirrors a changed title into the full-text index.
func UpdateBookFTSTitle(ctx context.Context, x dbq.DBTX, bookID int64, title string) error {
	return updateBookFTSColumn(ctx, x, bookID, "title", title)
}

// UpdateBookFTSAuthors mirrors a book's relinked authors into the full-text
// index. The value is the space-joined author list, not a single name.
func UpdateBookFTSAuthors(ctx context.Context, x dbq.DBTX, bookID int64, authors string) error {
	return updateBookFTSColumn(ctx, x, bookID, "authors", authors)
}

// UpdateBookFTSSeries mirrors a changed series name into the full-text index.
func UpdateBookFTSSeries(ctx context.Context, x dbq.DBTX, bookID int64, series string) error {
	return updateBookFTSColumn(ctx, x, bookID, "series", series)
}

// UpdateBookFTSAnnotation mirrors a changed annotation into the full-text index.
// The caller strips markup first (htmltext.StripMarkup) so the indexed tokens
// match what a reader sees.
func UpdateBookFTSAnnotation(ctx context.Context, x dbq.DBTX, bookID int64, annotation string) error {
	return updateBookFTSColumn(ctx, x, bookID, "annotation", annotation)
}

// updateBookFTSColumn sets one column of a book's full-text row. col is a
// package-internal constant at every call site, never user input; the value is
// parameterized.
func updateBookFTSColumn(ctx context.Context, x dbq.DBTX, bookID int64, col, value string) error {
	query, args, err := sqlite.Update(
		um.Table("books_fts"),
		um.SetCol(col).To(sqlite.Arg(value)),
		um.Where(sqlite.Quote("rowid").EQ(sqlite.Arg(bookID))),
	).Build(ctx)
	if err != nil {
		return fmt.Errorf("build fts %s update: %w", col, err)
	}
	if _, err := x.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("exec fts %s update: %w", col, err)
	}

	return nil
}
