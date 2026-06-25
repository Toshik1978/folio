package sync

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ingest"
)

// CoverExtractor pulls a book's cover bytes from its source file. Implemented by
// *ingest.Extractor and passed to New to enable cover-warming.
type CoverExtractor interface {
	Cover(ctx context.Context, bookID int64) (data []byte, ok bool, err error)
}

// MetadataBackfiller recovers and persists a book's offline metadata (annotation
// + identifiers) from its own file, at most once per book. *ingest.LocalBackfiller
// satisfies it; nil disables metadata backfill (cover-warming still runs).
type MetadataBackfiller interface {
	Fill(ctx context.Context, bookID int64) error
}

// warmer is the low-priority background pool: a single goroutine that drains
// per-library warm requests, caches missing covers, and backfills offline
// metadata, throttled so it never starves foreground sync I/O. It reads the
// engine's shared stop channel and closes done when its loop exits. Built only
// when an extractor is configured.
type warmer struct {
	log        *slog.Logger
	db         *sql.DB
	covers     ingest.CoverStore
	extractor  CoverExtractor
	backfiller MetadataBackfiller
	delay      time.Duration
	ch         chan int64
	stop       <-chan struct{} // engine's stop channel
	done       chan struct{}   // closed when run returns
}

// newWarmer builds a warmer over the engine's database, cover store, extractor,
// metadata backfiller, and stop channel. delay throttles between warm writes.
func newWarmer(
	log *slog.Logger,
	db *sql.DB,
	covers ingest.CoverStore,
	extractor CoverExtractor,
	backfiller MetadataBackfiller,
	stop <-chan struct{},
	delay time.Duration,
) *warmer {
	return &warmer{
		log:        log,
		db:         db,
		covers:     covers,
		extractor:  extractor,
		backfiller: backfiller,
		delay:      delay,
		ch:         make(chan int64, 8),
		stop:       stop,
		done:       make(chan struct{}),
	}
}

// enqueue schedules a low-priority cover-warming pass for a library. It never
// blocks; if the buffer is full the request is dropped (a later sync re-enqueues
// it).
func (w *warmer) enqueue(libraryID int64) {
	select {
	case w.ch <- libraryID:
	default:
	}
}

// run drains warm requests one library at a time until stop.
func (w *warmer) run() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		case id := <-w.ch:
			w.safeWarm(id)
		}
	}
}

// safeWarm runs a warm pass under a panic guard so an extractor/parser panic on
// the warm goroutine can never crash the process.
func (w *warmer) safeWarm(id int64) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("warm panicked", slog.Int64("library", id), slog.Any("panic", r))
		}
	}()
	w.warmLibrary(id)
}

// warmLibrary pre-extracts and caches covers for a library's books that have none
// cached yet, throttled so it doesn't starve foreground sync I/O. On-demand
// cover requests extract independently and are never blocked by this pass.
func (w *warmer) warmLibrary(libraryID int64) {
	ctx := context.Background()
	bookIDs, err := dbq.New(w.db).ListBookIDsByLibrary(ctx, libraryID)
	if err != nil {
		w.log.Error("warm: list books", slog.Int64("library", libraryID), slog.Any("error", err))
		return
	}

	warmed := 0
	for _, bookID := range bookIDs {
		if w.stopped() {
			return
		}
		if w.warmBook(ctx, bookID) {
			warmed++
			if !w.throttle() {
				return
			}
		}
	}
	if warmed > 0 {
		w.log.Info("warm: covers cached", slog.Int64("library", libraryID), slog.Int("count", warmed))
	}
}

// warmBook backfills one book's offline metadata and caches its cover if missing
// and extractable. It reports whether a new cover was written (the throttle key).
func (w *warmer) warmBook(ctx context.Context, id int64) bool {
	if w.backfiller != nil {
		// Not subject to the cover-write throttle: Fill is gated by metadata_checked
		// (cheap no-op after the first pass) and bounded by the write guard internally.
		_ = w.backfiller.Fill(ctx, id) // best-effort
	}
	if w.covers.Has(id) {
		return false
	}
	data, ok, err := w.extractor.Cover(ctx, id)
	if err != nil {
		w.log.Warn("warm: extract cover", slog.Int64("book", id), slog.Any("error", err))
		return false
	}
	if !ok {
		return false
	}
	if err := w.covers.Save(id, data); err != nil {
		w.log.Warn("warm: save cover", slog.Int64("book", id), slog.Any("error", err))
		return false
	}

	return true
}

// stopped reports whether the engine is shutting down.
func (w *warmer) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// throttle waits the warm delay, returning false if the engine stops first.
func (w *warmer) throttle() bool {
	if w.delay <= 0 {
		return true
	}
	select {
	case <-w.stop:
		return false
	case <-time.After(w.delay):
		return true
	}
}
