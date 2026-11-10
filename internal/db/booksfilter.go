package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

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
	Query     string      // free-text FTS across all columns; a token ending in * is a prefix match
	AnyTerms  []string    // OR group of terms (each a token or AND of words), ANDed with Query
	Title     FieldFilter // partial → title: FTS; exact → books.title =
	Author    FieldFilter // partial → authors: FTS; exact → authors.name =
	Series    FieldFilter // partial → series: FTS; exact → series.name =
	Genre     string      // exact genre/tag name (not FTS-indexed)
	Format    string      // file format, e.g. "epub"
	LibraryID int64       // when non-zero, restrict to this library
	Lang      string      // language code, e.g. "en"
	Publisher string      // exact publisher name
	YearFrom  int         // inclusive lower bound on books.year; 0 = unset
	YearTo    int         // inclusive upper bound on books.year; 0 = unset
	// Unreadable restricts to books whose metadata read failed at ReaderVersion
	// (a book_read_failures row, flow 'metadata'). Rows from an older reader
	// version are stale and do not count.
	Unreadable    bool
	ReaderVersion int64
	Sort          string // sort key: "source" → source-date desc, "rating" → rating desc, else imported_at desc
	Limit         int64
	Offset        int64
}

// bookColumns is the projection selected by FilterBooks: the complete books row,
// so a list-sourced dbq.Book is safe to use anywhere a GetBook row is. Its order
// must match the rows.Scan in scanBook (and the sqlc-generated dbq.Book layout);
// the TestFilterBooksScanMatchesGetBook guard fails if they drift.
var bookColumns = []any{ //nolint:gochecknoglobals // read-only column projection
	"b.id", "b.library_id", "b.library_key", "b.title", "b.series_id", "b.series_number",
	"b.language", "b.annotation", "b.metadata_checked", "b.enrichment_checked",
	"b.publisher", "b.publisher_fold", "b.year", "b.rating",
	"b.content_hash", "b.metadata_format", "b.added_at", "b.imported_at",
	"b.manually_matched", "b.cover_prio",
}

// FilterBooks returns the page of books matching f, ordered by relevance (when
// searching) or recency.
func FilterBooks(ctx context.Context, db *sql.DB, f BookFilter) ([]dbq.Book, error) {
	mods := f.filterMods()
	mods = append(mods, sm.Columns(bookColumns...))
	switch {
	case f.ftsMatch() != "":
		// The id tie-break keeps offset paging stable: books with equal column
		// lengths (e.g. INPX records without annotations) tie on bm25.
		mods = append(mods, sm.OrderBy("bm25(books_fts)"), sm.OrderBy("b.id").Desc())
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

// HasSearchTokens reports whether value yields at least one FTS token, i.e.
// contains a word that is not just punctuation or stars.
func HasSearchTokens(value string) bool {
	return len(ftsTokens(value)) > 0
}

// SearchTerm is one token of a search value as the FTS match uses it.
type SearchTerm struct {
	// Prefix is set for a token ending in *, which expands across the vocabulary.
	Prefix bool
	// PrefixRunes is, for a prefix token, the letters and digits of the word
	// the * applies to (the last word of the stem, as the tokenizer splits it):
	// the shorter it is, the more of the vocabulary it expands to.
	PrefixRunes int
}

// SearchTerms returns the tokens value contributes to a search, in order, so a
// caller can bound a request's cost before running it. Tokens dropped from the
// match (punctuation or stars only) are not returned.
func SearchTerms(value string) []SearchTerm {
	var out []SearchTerm
	for t := range strings.FieldsSeq(value) {
		if quoteSearchToken(t) == "" {
			continue
		}
		stem := strings.TrimRight(t, "*")
		if len(stem) == len(t) {
			out = append(out, SearchTerm{})
			continue
		}
		out = append(out, SearchTerm{Prefix: true, PrefixRunes: lastWordRunes(stem)})
	}

	return out
}

// ftsMatch assembles the single FTS5 MATCH expression from the free-text query
// and any non-exact facet filters. Returns "" when there is nothing to match.
func (f BookFilter) ftsMatch() string {
	var clauses []string
	add := func(c string) {
		if c != "" {
			clauses = append(clauses, c)
		}
	}
	add(ftsClause("", f.Query))
	add(anyClause(f.AnyTerms))
	if f.Title.search() {
		add(ftsClause("title", f.Title.Value))
	}
	if f.Author.search() {
		add(ftsClause("authors", f.Author.Value))
	}
	if f.Series.search() {
		add(ftsClause("series", f.Series.Value))
	}

	return strings.Join(clauses, " AND ")
}

// filterMatch is the FTS5 MATCH expression filterMods applies: ftsMatch plus,
// for an exact title, the title's own tokens scoped to the title column. books
// has no index on title, so the token match is what narrows an exact title to
// a few candidates; b.title = ? then keeps only the identical one. Every book
// write refreshes its FTS row, so the match never drops a book whose title is
// equal. It stays out of ftsMatch, which also decides relevance ordering: an
// exact title alone keeps the requested sort. A title with no tokens adds
// nothing and falls back to the plain comparison.
func (f BookFilter) filterMatch() string {
	m := f.ftsMatch()
	if !f.Title.Exact {
		return m
	}
	t := ftsClause("title", f.Title.Value)
	switch {
	case t == "":
		return m
	case m == "":
		return t
	default:
		return m + " AND " + t
	}
}

// filterMods builds the FROM clause plus the joins and WHERE conditions required
// by the active filters, shared by FilterBooks and CountFilteredBooks. Every
// caller-supplied value is bound as an argument via sqlite.Arg / sqlite.Raw.
//
// Exact author/series/genre/publisher filters match on the name and on its
// fold. The fold term is what an index serves (the UNIQUE name_fold, or
// idx_books_publisher_fold); the name term keeps the match exact, so a case
// variant of the name still finds nothing. The pair selects exactly what the
// name alone would, because every write stores name_fold = Fold(name).
// Without the fold term the name has no index, and the planner, unless
// ANALYZE statistics steer it, may walk every book_authors row to evaluate it.
func (f BookFilter) filterMods() []bob.Mod[*dialect.SelectQuery] {
	mods := []bob.Mod[*dialect.SelectQuery]{sm.From("books b")}

	if m := f.filterMatch(); m != "" {
		// A single table-level MATCH searches all requested FTS columns at once
		// (FTS5 forbids multiple MATCH constraints against the same table).
		mods = append(
			mods,
			// books_fts is keyed by rowid = books.id (migration 005), so this joins
			// on an integer key with no per-row CAST.
			sm.InnerJoin("books_fts").On(sqlite.Raw("b.id = books_fts.rowid")),
			sm.Where(sqlite.Raw("books_fts MATCH ?", m)),
		)
	}
	if f.Author.Exact {
		mods = append(
			mods,
			sm.InnerJoin("book_authors ba").On(sqlite.Quote("ba", "book_id").EQ(sqlite.Quote("b", "id"))),
			sm.InnerJoin("authors a").On(sqlite.Quote("a", "id").EQ(sqlite.Quote("ba", "author_id"))),
			sm.Where(sqlite.Quote("a", "name_fold").EQ(sqlite.Arg(Fold(f.Author.Value)))),
			sm.Where(sqlite.Quote("a", "name").EQ(sqlite.Arg(f.Author.Value))),
		)
	}
	if f.Series.Exact {
		mods = append(
			mods,
			sm.InnerJoin("series s").On(sqlite.Quote("s", "id").EQ(sqlite.Quote("b", "series_id"))),
			sm.Where(sqlite.Quote("s", "name_fold").EQ(sqlite.Arg(Fold(f.Series.Value)))),
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
			sm.Where(sqlite.Quote("g", "name_fold").EQ(sqlite.Arg(Fold(f.Genre)))),
			sm.Where(sqlite.Quote("g", "name").EQ(sqlite.Arg(f.Genre))),
		)
	}
	mods = append(mods, f.scalarMods()...)

	return mods
}

// scalarMods builds the plain column constraints (library, format, language,
// year range, publisher); each value is bound as an argument.
func (f BookFilter) scalarMods() []bob.Mod[*dialect.SelectQuery] {
	var mods []bob.Mod[*dialect.SelectQuery]

	if f.LibraryID != 0 {
		mods = append(mods, sm.Where(sqlite.Quote("b", "library_id").EQ(sqlite.Arg(f.LibraryID))))
	}
	if f.Format != "" {
		// EXISTS avoids JOIN fan-out for multi-format books.
		mods = append(mods, sm.Where(sqlite.Raw(
			"EXISTS (SELECT 1 FROM book_files bf WHERE bf.book_id = b.id AND bf.file_format = ?)", f.Format,
		)))
	}
	if f.Unreadable {
		mods = append(mods, sm.Where(sqlite.Raw(
			"EXISTS (SELECT 1 FROM book_read_failures f WHERE f.book_id = b.id "+
				"AND f.flow = 'metadata' AND f.reader_version = ?)",
			f.ReaderVersion,
		)))
	}
	if f.Lang != "" {
		mods = append(mods, sm.Where(sqlite.Quote("b", "language").EQ(sqlite.Arg(f.Lang))))
	}
	if f.YearFrom != 0 {
		mods = append(mods, sm.Where(sqlite.Quote("b", "year").GTE(sqlite.Arg(f.YearFrom))))
	}
	if f.YearTo != 0 {
		mods = append(mods, sm.Where(sqlite.Quote("b", "year").LTE(sqlite.Arg(f.YearTo))))
	}
	if f.Publisher != "" {
		mods = append(mods,
			sm.Where(sqlite.Quote("b", "publisher_fold").EQ(sqlite.Arg(Fold(f.Publisher)))),
			sm.Where(sqlite.Quote("b", "publisher").EQ(sqlite.Arg(f.Publisher))),
		)
	}

	return mods
}

// quoteToken wraps a single token as an FTS5 string literal, escaping embedded
// double quotes so user input cannot inject FTS query operators.
func quoteToken(tok string) string {
	return `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
}

// quoteSearchToken quotes one whitespace-delimited token for FTS5. A token with
// trailing * becomes a prefix query ("stem"*, the * outside the quotes); the
// stem is quoted like any other token, so quotes in it cannot inject syntax.
// Returns "" for a token with no letter, digit or private-use rune once the
// trailing *s are removed (a lone "*", "—", "-*"): the tokenizer would turn it
// into an empty phrase, which inside an AND matches nothing and zeroes the
// whole search, so it is dropped instead.
func quoteSearchToken(tok string) string {
	stem := strings.TrimRight(tok, "*")
	if !strings.ContainsFunc(stem, isFTSWordRune) {
		return ""
	}
	if len(stem) < len(tok) {
		return quoteToken(stem) + "*"
	}

	return quoteToken(tok)
}

// lastWordRunes counts the word runes of the last word in s, ignoring any
// trailing separators: for "co-op." it is 2 ("op").
func lastWordRunes(s string) int {
	rs := []rune(s)
	end := len(rs)
	for end > 0 && !isFTSWordRune(rs[end-1]) {
		end--
	}
	start := end
	for start > 0 && isFTSWordRune(rs[start-1]) {
		start--
	}

	return end - start
}

// isFTSWordRune reports whether r is part of a token for the unicode61 tokenizer.
func isFTSWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r)
}

// ftsTokens splits value on whitespace and quotes each surviving token.
func ftsTokens(value string) []string {
	var out []string
	for t := range strings.FieldsSeq(value) {
		if q := quoteSearchToken(t); q != "" {
			out = append(out, q)
		}
	}

	return out
}

// ftsClause builds an AND-of-tokens FTS5 expression for value, optionally scoped
// to a single column (empty col = all columns). Returns "" when value has no
// tokens. The whole clause is parenthesized so clauses compose under AND.
func ftsClause(col, value string) string {
	quoted := ftsTokens(value)
	if len(quoted) == 0 {
		return ""
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

// anyClause builds the OR group for terms: "(t1 OR t2 ...)". A multi-word term
// is the AND of its words (all must appear, in any order). Blank terms are
// dropped; returns "" when none remain.
func anyClause(terms []string) string {
	var alts []string
	for _, term := range terms {
		toks := ftsTokens(term)
		switch len(toks) {
		case 0:
		case 1:
			alts = append(alts, toks[0])
		default:
			alts = append(alts, "("+strings.Join(toks, " AND ")+")")
		}
	}
	if len(alts) == 0 {
		return ""
	}

	return "(" + strings.Join(alts, " OR ") + ")"
}

// scanBook reads one books row in bookColumns order into a dbq.Book.
func scanBook(rows *sql.Rows) (dbq.Book, error) {
	var b dbq.Book
	err := rows.Scan(
		&b.ID, &b.LibraryID, &b.LibraryKey, &b.Title, &b.SeriesID, &b.SeriesNumber,
		&b.Language, &b.Annotation, &b.MetadataChecked, &b.EnrichmentChecked,
		&b.Publisher, &b.PublisherFold, &b.Year, &b.Rating,
		&b.ContentHash, &b.MetadataFormat, &b.AddedAt, &b.ImportedAt,
		&b.ManuallyMatched, &b.CoverPrio,
	)
	if err != nil {
		return dbq.Book{}, fmt.Errorf("scan book: %w", err)
	}

	return b, nil
}
