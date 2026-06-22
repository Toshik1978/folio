package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// WriteGuard serializes write transactions across the whole process. SQLite
// permits a single writer at a time; even in WAL mode two connections that both
// issue BEGIN IMMEDIATE collide and one fails with SQLITE_BUSY once busy_timeout
// elapses. The guard makes the single-writer invariant explicit so writers queue
// on a Go mutex instead of racing at the SQLite layer. Readers do NOT take the
// guard — WAL lets them run concurrently with the one writer.
type WriteGuard struct {
	mu sync.Mutex
}

// NewWriteGuard returns a ready guard. Exactly one instance is shared by every
// writer (the sync engine and the API write handlers).
func NewWriteGuard() *WriteGuard { return &WriteGuard{} }

// Lock acquires the write lock. The caller MUST hold it for the full duration of
// its write transaction (BEGIN through COMMIT/ROLLBACK) and release it via Unlock.
func (g *WriteGuard) Lock() { g.mu.Lock() }

// Unlock releases the write lock.
func (g *WriteGuard) Unlock() { g.mu.Unlock() }

// WithTx runs fn inside a write transaction while holding the guard, committing
// on success and rolling back on error or panic.
func (g *WriteGuard) WithTx(ctx context.Context, sqldb *sql.DB, fn func(*sql.Tx) error) (err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

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
