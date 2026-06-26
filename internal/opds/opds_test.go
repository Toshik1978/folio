package opds

import (
	"context"
	"database/sql"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/samber/lo"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/auth"
	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
)

// TestOPDS is the package's single entry point; every suite is registered here.
func TestOPDS(t *testing.T) {
	suite.Run(t, new(feedsSuite))
	suite.Run(t, new(authSuite))
	suite.Run(t, new(downloadSuite))
}

const (
	testUser = "reader"
	testPass = "s3cret"
)

// baseSuite wires a fresh folio DB, cover store, handler, and router.
type baseSuite struct {
	suite.Suite

	dir     string
	db      *sql.DB
	q       *dbq.Queries
	covers  *covers.Store
	authn   *auth.Authenticator
	handler *Handler
	router  http.Handler
}

func (s *baseSuite) SetupTest() {
	s.dir = s.T().TempDir()

	database, err := db.Open(slog.New(slog.DiscardHandler), s.dir)
	s.Require().NoError(err)
	store, err := covers.NewStore(s.dir, nil, ingest.NewCoverState(database))
	s.Require().NoError(err)

	s.db = database
	s.q = dbq.New(database)
	s.covers = store
	s.authn = auth.New(slog.New(slog.DiscardHandler), database)
	s.handler = New(slog.New(slog.DiscardHandler), database, store, s.authn, "")

	r := chi.NewRouter()
	s.handler.Register(r)
	s.router = r
}

func (s *baseSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *baseSuite) setCreds() {
	s.Require().NoError(s.authn.SetCredentials(context.Background(), new(testUser), new(testPass)))
}

// get issues a GET against the /opds sub-router (paths omit the /opds prefix).
func (s *baseSuite) get(path string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

// getAuth issues a GET with HTTP Basic Auth credentials.
func (s *baseSuite) getAuth(path, user, pass string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	r.SetBasicAuth(user, pass)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

func (s *baseSuite) seedSource(typ, path string) int64 {
	id, err := s.q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Type: typ, Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	return id
}

type bookSeed struct {
	Title      string
	Format     string
	SourcePath string
	Authors    []string
	Genres     []string
	Series     string
	Annotation string
	Publisher  string
	Year       int
}

// firstFileID returns the id of a book's first file (each seed inserts one).
func (s *baseSuite) firstFileID(bookID int64) int64 {
	files, err := s.q.ListFilesForBook(context.Background(), bookID)
	s.Require().NoError(err)
	s.Require().NotEmpty(files)
	return files[0].ID
}

func (s *baseSuite) seedBook(libraryID int64, sd bookSeed) int64 {
	ctx := context.Background()
	sd.Format = lo.CoalesceOrEmpty(sd.Format, "epub")

	var seriesID sql.NullInt64
	if sd.Series != "" {
		id, err := s.q.InsertSeries(ctx, dbq.InsertSeriesParams{Name: sd.Series, NameFold: db.Fold(sd.Series)})
		s.Require().NoError(err)
		seriesID = sql.NullInt64{Int64: id, Valid: true}
	}

	key := lo.CoalesceOrEmpty(sd.SourcePath, sd.Title)
	bookID, err := s.q.InsertBook(ctx, dbq.InsertBookParams{
		Title: sd.Title, LibraryID: libraryID, LibraryKey: key,
		SeriesID: seriesID, Language: "en",
		Annotation: nullStr(sd.Annotation), Publisher: nullStr(sd.Publisher),
		PublisherFold: db.FoldNull(nullStr(sd.Publisher)),
		Year:          nullInt(sd.Year), ContentHash: key, AddedAt: time.Now().UnixNano(),
	})
	s.Require().NoError(err)
	if _, err := s.q.InsertBookFile(ctx, dbq.InsertBookFileParams{
		BookID: bookID, FileFormat: sd.Format, FileSize: 1, SourcePath: key,
	}); err != nil {
		s.Require().NoError(err)
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
		BookID: strconv.FormatInt(bookID, 10), Title: sd.Title,
		Authors: strings.Join(sd.Authors, " "), Series: sd.Series, Annotation: sd.Annotation,
	}))

	return bookID
}

// parseFeed unmarshals an OPDS feed response body.
func (s *baseSuite) parseFeed(w *httptest.ResponseRecorder) feed {
	var f feed
	s.Require().NoError(xml.Unmarshal(w.Body.Bytes(), &f))
	return f
}

func nullStr(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}
