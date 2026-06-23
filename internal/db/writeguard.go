package db

import (
	"context"
	"database/sql"
	"fmt"
)

// WriteGuard serializes write transactions across the whole process. SQLite
// permits a single writer at a time; even in WAL mode two connections that both
// issue BEGIN IMMEDIATE collide and one fails with SQLITE_BUSY once busy_timeout
// elapses. The guard makes the single-writer invariant explicit so writers queue
// on it instead of racing at the SQLite layer. Readers do NOT take the guard —
// WAL lets them run concurrently with the one writer.
//
// Acquisition is context-aware: Lock returns the context's error if it cannot
// take the guard before the context is done. This lets a short-lived HTTP write
// give up (a bounded budget, mapped to 503) rather than block indefinitely behind
// a long indexing run and overrun the server WriteTimeout, while a background
// writer (a sync or purge) passes a lifecycle context and waits as long as it
// takes — cancelled only at shutdown.
//
// It is implemented as a capacity-1 channel rather than a sync.Mutex precisely so
// the wait can be selected against a context. The trade-off versus sync.Mutex is
// that an unbalanced Unlock blocks instead of panicking; callers MUST pair every
// successful Lock with exactly one Unlock (a deferred Unlock is the norm).
type WriteGuard struct {
	ch chan struct{}
}

// NewWriteGuard returns a ready guard. Exactly one instance is shared by every
// writer (the sync engine and the API write handlers).
func NewWriteGuard() *WriteGuard { return &WriteGuard{ch: make(chan struct{}, 1)} }

// Lock acquires the write lock, blocking until it is free or ctx is done. It
// returns nil once held (the caller MUST hold it for the full duration of its
// write transaction — BEGIN through COMMIT/ROLLBACK — and release it via Unlock),
// or ctx.Err() if ctx is cancelled or times out first, in which case the guard is
// NOT held and Unlock must not be called.
func (g *WriteGuard) Lock(ctx context.Context) error {
	select {
	case g.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		// Return the raw cause so callers can errors.Is it against
		// context.DeadlineExceeded / Canceled; callers annotate as needed.
		return ctx.Err() //nolint:wrapcheck // intentional: expose the context cause
	}
}

// Unlock releases the write lock. It must be called exactly once after a Lock
// that returned nil.
func (g *WriteGuard) Unlock() { <-g.ch }

// WithTx runs fn inside a write transaction while holding the guard, committing
// on success and rolling back on error or panic. It honors ctx for acquisition:
// if the guard cannot be taken before ctx is done it returns ctx.Err() without
// opening a transaction.
func (g *WriteGuard) WithTx(ctx context.Context, sqldb *sql.DB, fn func(*sql.Tx) error) (err error) {
	if lerr := g.Lock(ctx); lerr != nil {
		return lerr
	}
	defer g.Unlock()

	tx, err := sqldb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	err = fn(tx)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit write tx: %w", err)
	}

	return nil
}
