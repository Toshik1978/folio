package sync

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	stdsync "sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/covers"
	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
	"github.com/Toshik1978/folio/internal/libtype"
)

// TestSync is the package's single entry point; every suite is registered here.
func TestSync(t *testing.T) {
	suite.Run(t, new(engineSuite))
	suite.Run(t, new(schedulerSuite))
	suite.Run(t, new(watcherSuite))
	suite.Run(t, new(warmSuite))
	suite.Run(t, new(reporterSuite))
	suite.Run(t, new(engineEventsSuite))
}

// baseSuite gives each test a fresh folio database, cover store, and a stub
// parser registry wired into an engine.
type baseSuite struct {
	suite.Suite

	db        *sql.DB
	store     *covers.Store
	parser    *stubParser
	engine    *Engine
	extractor CoverExtractor // nil unless a suite opts into cover-warming
}

func (s *baseSuite) SetupTest() {
	dir := s.T().TempDir()

	database, err := db.Open(slog.New(slog.DiscardHandler), dir)
	s.Require().NoError(err)

	store, err := covers.NewStore(dir, nil)
	s.Require().NoError(err)

	parser := &stubParser{result: ingest.Result{Added: 1}}
	engine, err := New(slog.New(slog.DiscardHandler), database, db.NewWriteGuard(), map[string]Parser{
		"stub":         parser,
		libtype.Folder: parser,
		libtype.INPX:   parser,
	}, store, s.extractor)
	s.Require().NoError(err)
	engine.debounce = 20 * time.Millisecond // keep watcher tests fast

	s.db = database
	s.store = store
	s.parser = parser
	s.engine = engine
}

func (s *baseSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// jpegFixture builds a tiny real JPEG so cover Saves (which transcode to JPEG)
// accept it. Distinct colors yield distinct bytes, letting "untouched"
// assertions tell two covers apart.
func (s *baseSuite) jpegFixture(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: r, G: g, B: b, A: 255})
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func (s *baseSuite) insertLibrary(typ, path string) dbq.Library {
	q := dbq.New(s.db)
	id, err := q.InsertLibrary(context.Background(), dbq.InsertLibraryParams{
		Type: typ, Path: path, SyncIntervalSeconds: 3600, CreatedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	src, err := q.GetLibrary(context.Background(), id)
	s.Require().NoError(err)

	return src
}

func (s *baseSuite) getLibrary(id int64) dbq.Library {
	src, err := dbq.New(s.db).GetLibrary(context.Background(), id)
	s.Require().NoError(err)
	return src
}

func (s *baseSuite) markPendingPurge(id int64, purgeAt sql.NullInt64) {
	err := dbq.New(s.db).UpdateLibraryStatus(context.Background(), dbq.UpdateLibraryStatusParams{
		Status: statusPendingPurge, PurgeAt: purgeAt, ID: id,
	})
	s.Require().NoError(err)
}

// stubParser is a configurable Parser that records its invocations and
// tracks peak concurrency so the worker's single-writer guarantee can be
// asserted.
type stubParser struct {
	result     ingest.Result
	err        error
	delay      time.Duration
	checkpoint string // when non-empty, the engine gates on this fingerprint
	onSync     func(ctx context.Context, source dbq.Library, database *sql.DB, coverStore ingest.CoverStore)
	panicMsg   string // when set, Sync panics with this message

	mu          stdsync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	synced      []int64 // source IDs in call order
}

func (p *stubParser) Sync(
	ctx context.Context, source dbq.Library, database *sql.DB, coverStore ingest.CoverStore, _ ingest.Reporter,
) (ingest.Result, error) {
	p.mu.Lock()
	p.calls++
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	p.synced = append(p.synced, source.ID)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()

	if p.panicMsg != "" {
		panic(p.panicMsg)
	}

	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if p.onSync != nil {
		p.onSync(ctx, source, database, coverStore)
	}

	return p.result, p.err
}

// Checkpoint makes stubParser an Checkpointer. A zero value disables
// gating (mirrors a parser that cannot fingerprint its source).
func (p *stubParser) Checkpoint(dbq.Library) (string, error) {
	return p.checkpoint, nil
}

func (p *stubParser) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *stubParser) peakConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight
}
