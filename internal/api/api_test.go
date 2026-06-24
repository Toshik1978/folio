package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/samber/lo"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/htmltext"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/metasearch"
	"github.com/Toshik1978/folio/internal/sync"
)

// TestAPI is the package's single entry point; every suite is registered here.
func TestAPI(t *testing.T) {
	suite.Run(t, new(booksSuite))
	suite.Run(t, new(matchSuite))
	suite.Run(t, new(enrichSuite))
	suite.Run(t, new(librariesSuite))
	suite.Run(t, new(metaSuite))
	suite.Run(t, new(listsSuite))
	suite.Run(t, new(contractSuite))
	suite.Run(t, new(editSuite))
	suite.Run(t, new(utilSuite))
	suite.Run(t, new(coverSearchSuite))
	suite.Run(t, new(lettersBoundsSuite))
}

// fakeSync is a lightweight api.SyncEngine for handler tests.
type fakeSync struct {
	triggeredAll       int
	triggeredAllForced int
	triggered          []int64 // non-forced TriggerLibrary calls
	triggeredForced    []int64 // forced TriggerLibraryForced calls
	purged             []int64
	rescheduled        int
	rescheduleErr      error
	status             sync.Status
}

func (f *fakeSync) TriggerAll()                   { f.triggeredAll++ }
func (f *fakeSync) TriggerAllForced()             { f.triggeredAllForced++ }
func (f *fakeSync) TriggerLibrary(id int64)       { f.triggered = append(f.triggered, id) }
func (f *fakeSync) TriggerLibraryForced(id int64) { f.triggeredForced = append(f.triggeredForced, id) }
func (f *fakeSync) Status() sync.Status           { return f.status }
func (f *fakeSync) Reschedule(context.Context) error {
	f.rescheduled++
	return f.rescheduleErr
}

func (f *fakeSync) RequestPurge(id int64) {
	f.purged = append(f.purged, id)
}

// fakeExtractor is an api.MetadataExtractor returning fixed backfill metadata.
type fakeExtractor struct {
	annotation  string
	identifiers []ebook.Identifier
	called      int
}

func (f *fakeExtractor) Backfill(context.Context, int64) (ebook.Metadata, bool, error) {
	f.called++
	if f.annotation == "" && len(f.identifiers) == 0 {
		return ebook.Metadata{}, false, nil
	}
	return ebook.Metadata{Annotation: f.annotation, Identifiers: f.identifiers}, true, nil
}

// fakeEnricher is an api.MetadataEnricher returning fixed online metadata.
type fakeEnricher struct {
	meta       ebook.Metadata
	ok         bool
	called     int
	candidates []metasearch.Volume
	applyMeta  ebook.Metadata
	lastQuery  string
	lastSource string
	lastVolume string
}

func (f *fakeEnricher) Enrich(context.Context, int64) (ebook.Metadata, bool, error) {
	f.called++
	return f.meta, f.ok, nil
}

func (f *fakeEnricher) Search(_ context.Context, query string) ([]metasearch.Volume, error) {
	f.lastQuery = query
	return f.candidates, nil
}

func (f *fakeEnricher) ApplyMatch(_ context.Context, source, id string) (ebook.Metadata, error) {
	f.lastSource = source
	f.lastVolume = id
	return f.applyMeta, nil
}

// baseSuite wires a fresh folio DB, cover store, fake engine, and router.
type baseSuite struct {
	suite.Suite

	dir       string
	db        *sql.DB
	q         *dbq.Queries
	guard     *db.WriteGuard
	covers    *covers.Store
	sync      *fakeSync
	broker    *events.Broker
	books     *BooksHandler
	catalog   *CatalogHandler
	libraries *LibrariesHandler
	syncH     *SyncHandler
	router    http.Handler
}

func (s *baseSuite) SetupTest() {
	s.dir = s.T().TempDir()

	database, err := db.Open(slog.New(slog.DiscardHandler), s.dir)
	s.Require().NoError(err)
	store, err := covers.NewStore(s.dir, nil)
	s.Require().NoError(err)

	s.db = database
	s.q = dbq.New(database)
	s.guard = db.NewWriteGuard()
	s.covers = store
	s.sync = &fakeSync{}
	s.broker = events.NewBroker()
	log := slog.New(slog.DiscardHandler)
	s.books = NewBooks(log, database, s.guard, store, nil, nil, store, nil)
	s.catalog = NewCatalog(log, database, ingest.CanonicalGenres())
	s.libraries = NewLibraries(log, database, s.guard, s.sync, "")
	s.syncH = NewSync(log, s.sync, s.broker)

	r := chi.NewRouter()
	s.books.Register(r)
	s.catalog.Register(r)
	s.libraries.Register(r)
	s.syncH.Register(r)
	s.router = r
}

func (s *baseSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// rebuildRouter re-registers all handlers on a fresh chi router. Call this
// after replacing s.books (or another handler) mid-test so the new handler is
// wired to s.router.
func (s *baseSuite) rebuildRouter() {
	r := chi.NewRouter()
	s.books.Register(r)
	s.catalog.Register(r)
	s.libraries.Register(r)
	s.syncH.Register(r)
	s.router = r
}

// jpegFixture returns a tiny real JPEG, since the cover store transcodes covers
// on save and rejects non-image bytes.
func (s *baseSuite) jpegFixture() []byte {
	return s.jpegFixtureSized(2)
}

// jpegFixtureSized returns a real square JPEG of the given side length. Two
// fixtures of different sizes encode to distinguishable bytes, which lets a test
// tell one cover from another.
func (s *baseSuite) jpegFixtureSized(n int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// do issues a request against the mounted /api sub-router (paths omit /api).
func (s *baseSuite) do(method, path string, body any) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		buf, err := json.Marshal(body)
		s.Require().NoError(err)
		r = httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(buf))
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	return w
}

func (s *baseSuite) decode(w *httptest.ResponseRecorder, v any) {
	s.Require().NoError(json.Unmarshal(w.Body.Bytes(), v))
}

func (s *baseSuite) seedLibrary(typ, path string) int64 {
	id, err := s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Name: typ + " " + path, Type: typ, Path: path,
		SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)

	return id
}

// bookSeed describes a book to insert for a test.
type bookSeed struct {
	Title       string
	Format      string
	SourcePath  string
	Authors     []string
	Genres      []string
	Series      string
	Annotation  string
	Lang        string
	Size        int64
	Publisher   string
	Year        int
	Pages       int
	Rating      int
	Identifiers map[string]string // type -> value
}

// seedBook inserts a book row plus its author/genre links and FTS entry,
// mirroring what the ingest importer would produce.
func (s *baseSuite) seedBook(libraryID int64, sd bookSeed) int64 {
	ctx := context.Background()
	sd.Format = lo.CoalesceOrEmpty(sd.Format, "epub")
	sd.Lang = lo.CoalesceOrEmpty(sd.Lang, "en")

	var seriesID sql.NullInt64
	if sd.Series != "" {
		id, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: sd.Series, NameFold: db.Fold(sd.Series)})
		s.Require().NoError(err)
		seriesID = sql.NullInt64{Int64: id, Valid: true}
	}

	key := lo.CoalesceOrEmpty(sd.SourcePath, sd.Title+"#"+itoa(time.Now().UnixNano()))
	bookID, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		Title:         sd.Title,
		LibraryID:     libraryID,
		LibraryKey:    key,
		SeriesID:      seriesID,
		Language:      sd.Lang,
		Annotation:    toNull(sd.Annotation),
		Publisher:     toNull(sd.Publisher),
		PublisherFold: db.FoldNull(toNull(sd.Publisher)),
		Year:          toNullInt(sd.Year),
		Rating:        toNullInt(sd.Rating),
		ContentHash:   key,
		AddedAt:       time.Now().UnixNano(), // unique + monotonic for ordering
	})
	s.Require().NoError(err)

	if _, err := s.q.InsertBookFile(ctx, dbq.InsertBookFileParams{
		BookID:     bookID,
		FileFormat: sd.Format,
		FileSize:   sd.Size,
		SourcePath: sd.SourcePath,
		Pages:      toNullInt(sd.Pages),
	}); err != nil {
		s.Require().NoError(err)
	}

	for typ, val := range sd.Identifiers {
		s.Require().NoError(s.q.InsertBookIdentifier(ctx, dbq.InsertBookIdentifierParams{
			BookID: bookID, Type: typ, Value: val,
		}))
	}

	for _, name := range sd.Authors {
		aid, err := s.q.InsertAuthor(ctx, dbq.InsertAuthorParams{Name: name, NameFold: db.Fold(name)})
		s.Require().NoError(err)
		s.Require().NoError(s.q.InsertBookAuthor(ctx, dbq.InsertBookAuthorParams{BookID: bookID, AuthorID: aid}))
	}
	for _, name := range sd.Genres {
		gid, err := s.q.InsertGenre(ctx, dbq.InsertGenreParams{Name: name, NameFold: db.Fold(name)})
		s.Require().NoError(err)
		s.Require().NoError(s.q.InsertBookGenre(ctx, dbq.InsertBookGenreParams{BookID: bookID, GenreID: gid}))
	}

	s.Require().NoError(s.q.InsertBookFTS(ctx, dbq.InsertBookFTSParams{
		BookID:     itoa(bookID),
		Title:      sd.Title,
		Authors:    strings.Join(sd.Authors, " "),
		Series:     sd.Series,
		Annotation: htmltext.StripMarkup(sd.Annotation),
	}))

	return bookID
}

// firstFileID returns the id of a book's first file (each seed inserts one).
func (s *baseSuite) firstFileID(bookID int64) int64 {
	files, err := s.q.ListFilesForBook(context.Background(), bookID)
	s.Require().NoError(err)
	s.Require().NotEmpty(files)
	return files[0].ID
}

func toNull(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func toNullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}
