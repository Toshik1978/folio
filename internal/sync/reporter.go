package sync

import (
	"strconv"
	stdsync "sync"
	"time"

	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/ingest"
)

// nopReporter is the silent Reporter the engine uses when no event publisher is
// wired: parsers still call SetTotal/Add, the counts just go nowhere.
type nopReporter struct{}

func (nopReporter) SetTotal(int) {}
func (nopReporter) Add(int)      {}

// beginProgress returns the Reporter one sync run pushes progress to, together
// with a finish func to call when the run ends. With an event publisher wired it
// streams throttled progress and finish emits the final frame; without one both
// are inert — so runSync never branches on whether progress is enabled, nor holds
// the concrete reporter to flush it.
//
//nolint:ireturn // a factory that returns one of two Reporter impls behind the consumer's interface
func (e *Engine) beginProgress(library int64) (ingest.Reporter, func()) {
	if e.events == nil {
		return nopReporter{}, func() {}
	}
	r := newProgressReporter(e.events, library, e.now, defaultProgressInterval)

	return r, r.emit
}

// progressEvent is the payload of a TypeProgress event. Total is omitted when the
// parser does not know it (the frontend then renders an indeterminate count-up bar).
type progressEvent struct {
	Library   int64 `json:"library"`
	Processed int   `json:"processed"`
	Total     int   `json:"total,omitempty"`
}

// progressReporter implements ingest.Reporter for one library's sync. It throttles
// per-record Add emissions to at most one per interval (coalesced further by the
// broker), emits SetTotal immediately, and Flush always emits the final frame.
type progressReporter struct {
	pub      Publisher
	library  int64
	now      func() time.Time
	interval time.Duration

	// mu guards the counters below. A progressReporter is driven by the single sync
	// worker goroutine — one reporter per run, with Add called sequentially from the
	// reconciler — so the lock is defensive: the fields are safe, but the read-then-
	// emit gap in Add is intentionally NOT atomic. A redundant emit would be harmless
	// (the broker coalesces per library). Do not assume full atomicity, e.g. before
	// parallelizing a parser.
	mu        stdsync.Mutex
	processed int
	total     int
	lastEmit  time.Time
}

func newProgressReporter(
	pub Publisher,
	library int64,
	now func() time.Time,
	interval time.Duration,
) *progressReporter {
	return &progressReporter{pub: pub, library: library, now: now, interval: interval}
}

// SetTotal records the expected record count and emits immediately so the UI can
// switch to a determinate bar. A total of 0 (never set) leaves the bar indeterminate.
func (r *progressReporter) SetTotal(n int) {
	r.mu.Lock()
	r.total = n
	r.mu.Unlock()
	r.emit()
}

// Add increments the processed count, emitting at most once per interval.
func (r *progressReporter) Add(n int) {
	r.mu.Lock()
	r.processed += n
	due := r.now().Sub(r.lastEmit) >= r.interval
	r.mu.Unlock()
	if due {
		r.emit()
	}
}

func (r *progressReporter) emit() {
	if r.pub == nil {
		return
	}
	r.mu.Lock()
	r.lastEmit = r.now()
	ev := progressEvent{Library: r.library, Processed: r.processed, Total: r.total}
	r.mu.Unlock()
	r.pub.Publish(events.Event{
		Type:        events.TypeProgress,
		CoalesceKey: "progress:" + strconv.FormatInt(r.library, 10),
		Data:        ev,
	})
}
