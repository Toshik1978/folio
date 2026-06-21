package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
)

// CalibreParser imports a Calibre library by reading its metadata.db over a
// read-only connection. library.Path points at the library root directory
// (the folder containing metadata.db); book files and cover images are
// resolved relative to it.
type CalibreParser struct {
	log *slog.Logger
}

// calibreAuthorsQuery joins authors per book.
const calibreAuthorsQuery = `
	SELECT bal.book, a.name
	FROM books_authors_link bal
			 JOIN authors a ON a.id = bal.author
	ORDER BY bal.id`

// calibreTagsQuery joins tags per book.
const calibreTagsQuery = `
	SELECT btl.book, t.name
	FROM books_tags_link btl
			 JOIN tags t ON t.id = btl.tag
	ORDER BY btl.id`

// calibrePublishersQuery joins publishers per book (Calibre allows only one).
const calibrePublishersQuery = `
	SELECT bpl.book, p.name
	FROM books_publishers_link bpl
			 JOIN publishers p ON p.id = bpl.publisher`

// calibreIdentifiersQuery lists typed identifiers per book.
const calibreIdentifiersQuery = `SELECT book, type, val FROM identifiers`

// calibreRatingsQuery joins the per-book rating (Calibre stores 0..10).
const calibreRatingsQuery = `
	SELECT brl.book, r.rating
	FROM books_ratings_link brl
			 JOIN ratings r ON r.id = brl.rating`

// calibreBooksQuery joins one row per (book, format) with its series, comment,
// and primary language.
const calibreBooksQuery = `
	SELECT b.id, b.title, b.path, b.series_index, b.has_cover, b.pubdate, b.timestamp,
	       d.format, d.name, d.uncompressed_size,
	       s.name AS series_name,
	       c.text AS comments,
	       (SELECT l.lang_code FROM books_languages_link bll
	          JOIN languages l ON l.id = bll.lang_code
	          WHERE bll.book = b.id ORDER BY bll.item_order LIMIT 1) AS lang
	FROM books b
	JOIN data d ON d.book = b.id
	LEFT JOIN books_series_link bsl ON bsl.book = b.id
	LEFT JOIN series s ON s.id = bsl.series
	LEFT JOIN comments c ON c.book = b.id
	ORDER BY b.id`

// calibreBooksCountQuery counts exactly the rows calibreBooksQuery yields, so the
// reporter's total matches the number of upsert calls and the UI shows a
// determinate bar. Wrapping the main query as a subquery keeps the two in sync
// automatically (no separate FROM/WHERE to maintain).
const calibreBooksCountQuery = `SELECT COUNT(*) FROM (` + calibreBooksQuery + `)`

// NewCalibreParser builds the Calibre library parser.
func NewCalibreParser(log *slog.Logger) *CalibreParser { return &CalibreParser{log} }

// Checkpoint fingerprints the library's metadata.db, which Calibre rewrites on
// any change, so the engine can skip an unchanged library.
func (*CalibreParser) Checkpoint(library dbq.Library) (string, error) {
	return fileCheckpoint(filepath.Join(library.Path, "metadata.db"))
}

func (c *CalibreParser) Sync(
	ctx context.Context,
	library dbq.Library,
	db *sql.DB,
	covers CoverStore,
	r Reporter,
) (Result, error) {
	metaPath := filepath.Join(library.Path, "metadata.db")
	// Open read-only without touching the external library. NB: use a plain
	// path with a query string, not a "file:" URI — modernc.org/sqlite
	// mis-reads "file:" URIs (returns an empty schema) once the process has
	// opened other SQLite databases.
	cdb, err := sql.Open("sqlite", metaPath+"?mode=ro")
	if err != nil {
		return Result{}, fmt.Errorf("open calibre db: %w", err)
	}
	defer func() { _ = cdb.Close() }()

	if total, cErr := calibreBookCount(ctx, cdb); cErr != nil {
		_ = cErr // non-fatal: without a total the UI shows an indeterminate bar
	} else if total > 0 {
		r.SetTotal(total)
	}

	links, err := loadCalibreLinks(ctx, cdb)
	if err != nil {
		return Result{}, err
	}

	rows, err := cdb.QueryContext(ctx, calibreBooksQuery) //nolint:rowserrcheck // false, err checked below
	if err != nil {
		return Result{}, fmt.Errorf("query books: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return runReconcile(ctx, db, covers, library, r, c.log, func(ctx context.Context, rc *reconciler) error {
		return reconcileCalibreRows(ctx, rc, rows, library.Path, library.ID, links)
	})
}

func reconcileCalibreRows(
	ctx context.Context, rc *reconciler, rows *sql.Rows,
	libraryPath string, libraryID int64, links calibreLinkSet,
) error {
	cr := &calibreCoverReader{libDir: libraryPath}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("calibre import canceled: %w", err)
		}
		rec, scanErr := scanCalibreRow(rows, libraryID, links, cr)
		if scanErr != nil {
			return scanErr
		}
		if upErr := rc.upsert(ctx, rec); upErr != nil {
			return upErr
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate calibre rows: %w", err)
	}

	return nil
}

// calibreLinkSet groups the per-book lookups shared across a Calibre sync.
type calibreLinkSet struct {
	authors     map[int64][]string
	genres      map[int64][]string
	publishers  map[int64][]string
	identifiers map[int64][]identifier
	ratings     map[int64]int // 1..5 stars
	pages       map[int64]int
}

// loadCalibreLinks reads the author, tag, publisher, identifier, rating, and
// page-count link tables.
func loadCalibreLinks(ctx context.Context, cdb *sql.DB) (calibreLinkSet, error) {
	authors, err := calibreLinks(ctx, cdb, calibreAuthorsQuery)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load authors: %w", err)
	}
	genres, err := calibreLinks(ctx, cdb, calibreTagsQuery)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load tags: %w", err)
	}
	publishers, err := calibreLinks(ctx, cdb, calibrePublishersQuery)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load publishers: %w", err)
	}
	identifiers, err := calibreIdentifiers(ctx, cdb)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load identifiers: %w", err)
	}
	ratings, err := loadCalibreRatings(ctx, cdb)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load ratings: %w", err)
	}
	pages, err := loadCalibrePages(ctx, cdb)
	if err != nil {
		return calibreLinkSet{}, fmt.Errorf("load pages: %w", err)
	}

	return calibreLinkSet{
		authors: authors, genres: genres, publishers: publishers,
		identifiers: identifiers, ratings: ratings, pages: pages,
	}, nil
}

// loadCalibreRatings maps each rated book to a 1..5 star value (Calibre stores
// 0..10; 4/6/8/10 → 2/3/4/5). Zero/absent ratings are omitted, as are half-star
// values below a full star (raw 1 = 0.5★, possible when allow_half_stars is on),
// which would otherwise round to a "valid 0" outside the 1..5 range.
func loadCalibreRatings(ctx context.Context, cdb *sql.DB) (map[int64]int, error) {
	rows, err := cdb.QueryContext(ctx, calibreRatingsQuery)
	if err != nil {
		return nil, fmt.Errorf("query calibre ratings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]int)
	for rows.Next() {
		var bookID, raw int64
		if err := rows.Scan(&bookID, &raw); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		if raw >= 2 {
			out[bookID] = int(raw / 2)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre ratings: %w", err)
	}

	return out, nil
}

// loadCalibrePages reads the per-book page count from Calibre's dynamic "pages"
// custom column. Returns an empty map when the library has no such column. The
// column id is looked up from custom_columns (trusted, not user input) before
// being interpolated into the custom_column_<id> table name.
func loadCalibrePages(ctx context.Context, cdb *sql.DB) (map[int64]int, error) {
	out := make(map[int64]int)

	var colID int64
	err := cdb.QueryRowContext(ctx, "SELECT id FROM custom_columns WHERE label = 'pages'").Scan(&colID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil // library has no 'pages' custom column
	}
	if err != nil {
		return nil, fmt.Errorf("query custom_columns for pages: %w", err)
	}

	rows, err := cdb.QueryContext(ctx, fmt.Sprintf("SELECT book, value FROM custom_column_%d", colID))
	if err != nil {
		return nil, fmt.Errorf("query custom_column_%d: %w", colID, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var bookID int64
		var val sql.NullInt64
		if err := rows.Scan(&bookID, &val); err != nil {
			return nil, fmt.Errorf("scan pages: %w", err)
		}
		if val.Valid && val.Int64 > 0 {
			out[bookID] = int(val.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pages: %w", err)
	}

	return out, nil
}

// parseCalibreTimestamp parses Calibre's books.timestamp (the date a book was
// added to the library) across the ISO-ish formats Calibre emits.
func parseCalibreTimestamp(str string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, str); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported calibre timestamp: %q", str)
}

// scanCalibreRow turns one joined metadata.db row into a bookRecord, resolving
// the file path, optional series, publisher, year, identifiers, and on-disk
// cover (read at most once per book via cr).
func scanCalibreRow(rows *sql.Rows, libraryID int64, links calibreLinkSet, cr *calibreCoverReader) (bookRecord, error) {
	var (
		bookID      int64
		title       string
		bookPath    string
		seriesIndex float64
		hasCover    int64
		pubdate     sql.NullString
		timestamp   sql.NullString
		format      string
		name        string
		size        sql.NullInt64
		seriesName  sql.NullString
		comments    sql.NullString
		lang        sql.NullString
	)
	if err := rows.Scan(&bookID, &title, &bookPath, &seriesIndex, &hasCover, &pubdate, &timestamp,
		&format, &name, &size, &seriesName, &comments, &lang); err != nil {
		return bookRecord{}, fmt.Errorf("scan book: %w", err)
	}

	ext := strings.ToLower(format)
	sourcePath := filepath.ToSlash(filepath.Join(bookPath, name+"."+ext))
	rec := bookRecord{
		LibraryID:   libraryID,
		LibraryKey:  fmt.Sprintf("calibre:%d", bookID),
		Title:       title,
		Authors:     links.authors[bookID],
		Genres:      links.genres[bookID],
		Annotation:  comments.String,
		Language:    normalizeLang(lang.String),
		Publisher:   firstOrEmpty(links.publishers[bookID]),
		Year:        calibreYear(pubdate),
		Pages:       links.pages[bookID],
		Identifiers: links.identifiers[bookID],
		SourcePath:  sourcePath,
		FileFormat:  ext,
		FileSize:    size.Int64,
	}
	if r, ok := links.ratings[bookID]; ok {
		rec.Rating = sql.NullInt64{Int64: int64(r), Valid: true}
	}
	if timestamp.Valid {
		if t, err := parseCalibreTimestamp(timestamp.String); err == nil {
			rec.AddedAt = t.Unix()
		}
	}
	if seriesName.Valid && seriesName.String != "" {
		rec.Series = seriesName.String
		rec.SeriesNumber = nullFloat(seriesIndex, true)
	}
	rec.Cover = cr.read(bookID, bookPath, hasCover != 0)

	return rec, nil
}

// calibreCoverReader reads each book's cover.jpg at most once per run: the books
// query yields one row per (book, format) ordered by book id, so a book's rows
// arrive consecutively and repeats skip the disk read (a 3-format book no longer
// reads — and re-saves — its cover three times).
type calibreCoverReader struct {
	libDir string
	lastID int64
}

func (cr *calibreCoverReader) read(bookID int64, bookPath string, hasCover bool) []byte {
	if !hasCover || bookID == cr.lastID {
		return nil
	}
	cr.lastID = bookID
	data, err := os.ReadFile(filepath.Join(cr.libDir, bookPath, "cover.jpg"))
	if err != nil {
		return nil
	}

	return data
}

// calibreIdentifiers groups (type, value) identifier rows by book id.
func calibreIdentifiers(ctx context.Context, db *sql.DB) (map[int64][]identifier, error) {
	rows, err := db.QueryContext(ctx, calibreIdentifiersQuery)
	if err != nil {
		return nil, fmt.Errorf("query calibre identifiers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]identifier)
	for rows.Next() {
		var bookID int64
		var typ, val string
		if err := rows.Scan(&bookID, &typ, &val); err != nil {
			return nil, fmt.Errorf("scan calibre identifier: %w", err)
		}
		out[bookID] = append(out[bookID], identifier{Type: typ, Value: val})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre identifiers: %w", err)
	}

	return out, nil
}

func firstOrEmpty(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// calibreYear extracts the publication year from Calibre's ISO pubdate via the
// shared ebook.ParseYear, which also rejects Calibre's "0101-01-01..." unknown
// sentinel (→ year 0, treated as unknown downstream).
func calibreYear(pubdate sql.NullString) int {
	if !pubdate.Valid {
		return 0
	}
	return ebook.ParseYear(pubdate.String)
}

// calibreBookCount executes calibreBooksCountQuery once to get a determinate
// progress total. See that constant for why it is derived from the main query.
func calibreBookCount(ctx context.Context, cdb *sql.DB) (int, error) {
	var n int
	if err := cdb.QueryRowContext(ctx, calibreBooksCountQuery).Scan(&n); err != nil {
		return 0, fmt.Errorf("count calibre books: %w", err)
	}
	return n, nil
}

// calibreLinks runs a (book_id, name) query and groups names by book id,
// preserving query order.
func calibreLinks(ctx context.Context, db *sql.DB, query string) (map[int64][]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query calibre links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]string)
	for rows.Next() {
		var bookID int64
		var name string
		if err := rows.Scan(&bookID, &name); err != nil {
			return nil, fmt.Errorf("scan calibre link: %w", err)
		}
		out[bookID] = append(out[bookID], name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calibre links: %w", err)
	}

	return out, nil
}
