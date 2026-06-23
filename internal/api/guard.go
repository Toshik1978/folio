package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/db"
)

// writeAcquireBudget bounds how long an HTTP write waits for the process-wide
// single-writer guard before giving up. It is short enough that a write can never
// approach the server WriteTimeout (60s) by queueing behind a long indexing run,
// yet long enough to absorb the sub-second contention between two ordinary API
// writes. It is a var, not a const, so tests can shrink it.
//
//nolint:gochecknoglobals // package-level tunable; overridden only in tests
var writeAcquireBudget = 2 * time.Second

// acquireWriteErr takes the single-writer guard with the write budget. On success
// it returns the release func (which the caller MUST call once, typically
// deferred) and a nil error; otherwise it returns the acquisition error —
// context.DeadlineExceeded when an indexing run held the guard past the budget, or
// context.Canceled when the caller's context was cancelled. It is the primitive
// behind acquireWrite/tryAcquireWrite; call it directly only where the error must
// propagate (e.g. a write helper that has no http.ResponseWriter of its own).
func (b base) acquireWriteErr(ctx context.Context, g *db.WriteGuard) (func(), error) {
	actx, cancel := context.WithTimeout(ctx, writeAcquireBudget)
	defer cancel()
	if err := g.Lock(actx); err != nil {
		return nil, fmt.Errorf("acquire write guard: %w", err)
	}

	return g.Unlock, nil
}

// acquireWrite takes the guard for a user-facing HTTP write. On success it returns
// the release func and ok=true (the caller MUST call release()). If an indexing
// run holds the guard past the budget it writes 503 and returns ok=false; if the
// client disconnected first it returns ok=false without writing.
func (b base) acquireWrite(ctx context.Context, w http.ResponseWriter, g *db.WriteGuard) (func(), bool) {
	release, err := b.acquireWriteErr(ctx, g)
	if err != nil {
		b.handleGuardErr(w, err)
		return nil, false
	}

	return release, true
}

// tryAcquireWrite takes the guard for a best-effort, write-on-read or secondary
// write. ok=false means an indexing run holds the guard; the caller should skip
// the write (no HTTP error is written) and rely on its retry-later path. The
// caller MUST call release() when ok.
func (b base) tryAcquireWrite(ctx context.Context, g *db.WriteGuard) (func(), bool) {
	release, err := b.acquireWriteErr(ctx, g)
	if err != nil {
		return nil, false
	}

	return release, true
}

// handleGuardErr maps a guard-acquisition error (from acquireWriteErr) onto an
// HTTP response for write paths that propagate the error themselves. It returns
// true when it recognized and handled err — writing 503 on a budget timeout, or
// nothing on client cancellation — and false when err is some other failure the
// caller should report itself.
func (b base) handleGuardErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		b.writeError(w, http.StatusServiceUnavailable, "indexing in progress; retry shortly")
		return true
	case errors.Is(err, context.Canceled):
		return true // client disconnected; the response is moot
	default:
		return false
	}
}
