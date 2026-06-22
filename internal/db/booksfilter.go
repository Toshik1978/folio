package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/sqlite"
	"github.com/stephenafamo/bob/dialect/sqlite/dialect"
	"github.com/stephenafamo/bob/dialect/sqlite/sm"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// FieldFilter is one facet's constraint. Exact selects an equality match on the
// underlying scalar/column; otherwise the value is token-matched via FTS5.
type FieldFilter struct {
	Value string
	Exact bool
}

func (f FieldFilter) search() bool { return f.Value != "" && !f.Exact }

// BookFilter describes the optional constraints applied to a book listing. Zero
// values mean "no constraint". Query plus any non-exact Title/Author/Series are
// AND-combined into a single FTS5 MATCH (results ranked by BM25); exact facets
// and the remaining fields are SQL equality constraints. With no FTS term the
// listing is ordered newest first.
type BookFilter struct {
	Query     string      // free-text FTS across all columns
	Title     FieldFilter // partial → title: FTS; exact → books.title =
	Author    FieldFilter // partial → authors: FTS; exact → authors.name =
	Series    FieldFilter // partial → series: FTS; exact → series.name =
	Genre     string      // exact genre/tag name (not FTS-indexed)
	Format    string      // file format, e.g. "epub"
	LibraryID int64       // when non-zero, restrict to this library
	Lang      string      // language code, e.g. "en"
	Publisher string      // exact publisher name
	Sort      string      // sort key: "source" → source-date desc, "rating" → rating desc, else imported_at desc
	Limit     int64
	Offset    int64
}

// bookColumns is the projection selected by FilterBooks: the complete books row,
// so a list-sourced dbq.Book is safe to use anywhere a GetBook row is. Its order
// must match the rows.Scan in scanBook (and the sqlc-generated dbq.Book layout);
// the TestFilterBooksScanMatchesGetBook guard fails if they drift.
var bookColumns = []any{ //nolint:gochecknoglobals
	"b.id", "b.library_id", "b.library_key", "b.title", "b.series_id", "b.series_number",
	"b.language", "b.annotation", "b.metadata_checked", "b.enrichment_checked",
	"b.publisher", "b.publisher_fold", "b.year", "b.rating",
	"b.content_hash", "b.metadata_format", "b.added_at", "b.imported_at",
	"b.manually_matched", "b.cover_prio",
}

// quoteToken wraps a single token as an FTS5 string literal, escaping embedded
// double quotes so user input cannot inject FTS query operators.
func quoteToken(tok string) string {
	return `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
}

// ftsClause builds an AND-of-tokens FTS5 expression for value, optionally scoped
// to a single column (empty col = all columns). Returns "" when value has no
// tokens. The whole clause is parenthesized so clauses compose under AND.
func ftsClause(col, value string) string {
	tokens := strings.Fields(value)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = quoteToken(t)
	}
	expr := strings.Join(quoted, " AND ")
	if len(quoted) > 1 {
		expr = "(" + expr + ")"
	}
	if col != "" {
		expr = "{" + col + "} : " + expr
	}

	return "(" + expr + ")"
}

// ftsMatch assembles the single FTS5 MATCH expression from the free-text query
// and any non-exact facet filters. Returns "" when there is nothing to match.
func (f BookFilter) ftsMatch() string {
	var clauses []string
	if c := ftsClause("", f.Query); c != "" {
		clauses = append(clauses, c)
	}
	if f.Title.search() {
		clauses = append(clauses, ftsClause("title", f.Title.Value))
	}
	if f.Author.search() {
		clauses = append(clauses, ftsClause("authors", f.Author.Value))
	}
	if f.Series.search() {
		clauses = append(clauses, ftsClause("series", f.Series.Value))
	}

	return strings.Join(clauses, " AND ")
}

// FilterBooks returns the page of books matching f, ordered by relevance (when
// searching) or recency.
func FilterBooks(ctx context.Context, db *sql.DB, f BookFilter) ([]dbq.Book, error) {
	mods := f.filterMods()
	mods = append(mods, sm.Columns(bookColumns...))
	switch {
	case f.ftsMatch() != "":
		mods = append(mods, sm.OrderBy("bm25(books_fts)"))
	case f.Sort == "rating":
		// Unrated books (NULL) sort last; ties broken by recency.
		mods = append(mods,
			sm.OrderBy("b.rating IS NULL"), sm.OrderBy("b.rating").Desc(),
			sm.OrderBy("b.added_at").Desc(), sm.OrderBy("b.id").Desc())
	case f.Sort == "source":
		// "Newest": source chronology (Calibre/INPX date, folder mtime).
		mods = append(mods, sm.OrderBy("b.added_at").Desc(), sm.OrderBy("b.id").Desc())
	default:
		// "Recently added": when the book entered Folio; ties broken by source date.
		mods = append(mods,
			sm.OrderBy("b.imported_at").Desc(),
			sm.OrderBy("b.added_at").Desc(), sm.OrderBy("b.id").Desc())
	}
	mods = append(mods, sm.Limit(sqlite.Arg(f.Limit)), sm.Offset(sqlite.Arg(f.Offset)))

	query, args, err := sqlite.Select(mods...).Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("build books query: %w", err)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []dbq.Book
	for rows.Next() {
		b, scanErr := scanBook(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate books: %w", err)
	}

	return items, nil
}

// CountFilteredBooks returns the total number of books matching f, ignoring its
// Limit/Offset (used for pagination metadata).
func CountFilteredBooks(ctx context.Context, db *sql.DB, f BookFilter) (int64, error) {
	mods := append(f.filterMods(), sm.Columns("COUNT(*)"))

	query, args, err := sqlite.Select(mods...).Build(ctx)
	if err != nil {
		return 0, fmt.Errorf("build count query: %w", err)
	}

	var n int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count books: %w", err)
	}

	return n, nil
}

// scanBook reads one books row in bookColumns order into a dbq.Book.
func scanBook(rows *sql.Rows) (dbq.Book, error) {
	var b dbq.Book
	if err := rows.Scan(
		&b.ID, &b.LibraryID, &b.LibraryKey, &b.Title, &b.SeriesID, &b.SeriesNumber,
		&b.Language, &b.Annotation, &b.MetadataChecked, &b.EnrichmentChecked,
		&b.Publisher, &b.PublisherFold, &b.Year, &b.Rating,
		&b.ContentHash, &b.MetadataFormat, &b.AddedAt, &b.ImportedAt,
		&b.ManuallyMatched, &b.CoverPrio,
	); err != nil {
		return dbq.Book{}, fmt.Errorf("scan book: %w", err)
	}

	return b, nil
}

// filterMods builds the FROM clause plus the joins and WHERE conditions required
// by the active filters, shared by FilterBooks and CountFilteredBooks. Every
// caller-supplied value is bound as an argument via sqlite.Arg / sqlite.Raw.
func (f BookFilter) filterMods() []bob.Mod[*dialect.SelectQuery] {
	mods := []bob.Mod[*dialect.SelectQuery]{sm.From("books b")}

	if m := f.ftsMatch(); m != "" {
		// A single table-level MATCH searches all requested FTS columns at once
		// (FTS5 forbids multiple MATCH constraints against the same table).
		mods = append(
			mods,
			sm.InnerJoin("books_fts").On(sqlite.Raw("b.id = CAST(books_fts.book_id AS INTEGER)")),
			sm.Where(sqlite.Raw("books_fts MATCH ?", m)),
		)
	}
	if f.Author.Exact {
		mods = append(
			mods,
			sm.InnerJoin("book_authors ba").On(sqlite.Quote("ba", "book_id").EQ(sqlite.Quote("b", "id"))),
			sm.InnerJoin("authors a").On(sqlite.Quote("a", "id").EQ(sqlite.Quote("ba", "author_id"))),
			sm.Where(sqlite.Quote("a", "name").EQ(sqlite.Arg(f.Author.Value))),
		)
	}
	if f.Series.Exact {
		mods = append(
			mods,
			sm.InnerJoin("series s").On(sqlite.Quote("s", "id").EQ(sqlite.Quote("b", "series_id"))),
			sm.Where(sqlite.Quote("s", "name").EQ(sqlite.Arg(f.Series.Value))),
		)
	}
	if f.Title.Exact {
		mods = append(mods, sm.Where(sqlite.Quote("b", "title").EQ(sqlite.Arg(f.Title.Value))))
	}
	if f.Genre != "" {
		mods = append(
			mods,
			sm.InnerJoin("book_genres bg").On(sqlite.Quote("bg", "book_id").EQ(sqlite.Quote("b", "id"))),
			sm.InnerJoin("genres g").On(sqlite.Quote("g", "id").EQ(sqlite.Quote("bg", "genre_id"))),
			sm.Where(sqlite.Quote("g", "name").EQ(sqlite.Arg(f.Genre))),
		)
	}
	if f.LibraryID != 0 {
		mods = append(mods, sm.Where(sqlite.Quote("b", "library_id").EQ(sqlite.Arg(f.LibraryID))))
	}
	if f.Format != "" {
		// EXISTS avoids JOIN fan-out for multi-format books.
		mods = append(mods, sm.Where(sqlite.Raw(
			"EXISTS (SELECT 1 FROM book_files bf WHERE bf.book_id = b.id AND bf.file_format = ?)", f.Format,
		)))
	}
	if f.Lang != "" {
		mods = append(mods, sm.Where(sqlite.Quote("b", "language").EQ(sqlite.Arg(f.Lang))))
	}
	if f.Publisher != "" {
		mods = append(mods, sm.Where(sqlite.Quote("b", "publisher").EQ(sqlite.Arg(f.Publisher))))
	}

	return mods
}
