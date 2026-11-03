package db

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stretchr/testify/suite"
)

// writeGuardSuite exercises the process-wide single-writer guard.
type writeGuardSuite struct {
	suite.Suite
}

// TestLockSerializesWriters asserts the guard admits at most one writer at a
// time: with eight goroutines contending, the peak concurrent holder count must
// be exactly one.
func (s *writeGuardSuite) TestLockSerializesWriters() {
	g := NewWriteGuard()
	var active atomic.Int32
	var maxActive atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s.Require().NoError(g.Lock(context.Background()))
			n := active.Add(1)
			if n > maxActive.Load() {
				maxActive.Store(n)
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			g.Unlock()
		})
	}
	wg.Wait()
	s.Equal(int32(1), maxActive.Load()) // never two writers inside the guard at once
}

// TestLockOnFreeGuardSucceeds asserts acquiring an uncontended guard returns nil.
func (s *writeGuardSuite) TestLockOnFreeGuardSucceeds() {
	g := NewWriteGuard()
	s.Require().NoError(g.Lock(context.Background()))
	g.Unlock()
}

// TestLockTimesOutWhileHeld asserts a writer that cannot acquire the guard before
// its context deadline gives up with DeadlineExceeded rather than blocking
// forever — the property that stops an API write from queueing behind a long
// indexing run past the HTTP WriteTimeout.
func (s *writeGuardSuite) TestLockTimesOutWhileHeld() {
	g := NewWriteGuard()
	s.Require().NoError(g.Lock(context.Background())) // holder keeps the guard
	defer g.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := g.Lock(ctx)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Less(time.Since(start), time.Second) // gave up promptly, did not block
}

// TestLockAcquiresAfterRelease asserts a waiter blocked on a held guard proceeds
// once the holder releases, well within its deadline.
func (s *writeGuardSuite) TestLockAcquiresAfterRelease() {
	g := NewWriteGuard()
	s.Require().NoError(g.Lock(context.Background()))

	go func() {
		time.Sleep(10 * time.Millisecond)
		g.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.Require().NoError(g.Lock(ctx)) // blocks ~10ms, then acquires
	g.Unlock()
}

// TestLockHonorsCancellation asserts an already-cancelled context aborts the wait
// with Canceled, distinguishing a client disconnect (no retry) from a budget
// timeout (retryable busy).
func (s *writeGuardSuite) TestLockHonorsCancellation() {
	g := NewWriteGuard()
	s.Require().NoError(g.Lock(context.Background()))
	defer g.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Require().ErrorIs(g.Lock(ctx), context.Canceled)
}

// recoverTx runs fn and returns the value it panicked with (nil if none).
func recoverTx(fn func()) (got any) {
	defer func() { got = recover() }()
	fn()

	return nil
}

// TestPanicRollsBackAndWraps asserts a panic inside a write transaction rolls
// back, surfaces as a *TxPanic carrying the original value and a stack, wraps
// only once across nested WithTx calls, unwraps to an error value, and releases
// the guard.
func (s *writeGuardSuite) TestPanicRollsBackAndWraps() {
	database, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	defer database.Close()
	ctx := context.Background()
	_, err = database.ExecContext(ctx, `CREATE TABLE guard_probe (v INTEGER)`)
	s.Require().NoError(err)

	g := NewWriteGuard()
	got := recoverTx(func() {
		_ = g.WithTx(ctx, database, func(tx *sql.Tx) error {
			_, ierr := tx.ExecContext(ctx, `INSERT INTO guard_probe (v) VALUES (1)`)
			s.Require().NoError(ierr)
			panic("boom")
		})
	})
	var tp *TxPanic
	s.Require().ErrorAs(asError(got), &tp)
	s.Equal("boom", tp.Value)
	s.NotEmpty(tp.Stack)
	s.Equal(tp.Stack, tp.CrashStack())
	s.Equal("database write path", tp.CrashPlace())
	s.Equal("panic in write transaction: boom", tp.Error())
	s.NoError(tp.Unwrap())

	var n int
	s.Require().NoError(database.QueryRowContext(ctx, `SELECT COUNT(*) FROM guard_probe`).Scan(&n))
	s.Zero(n)

	lctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	s.Require().NoError(g.Lock(lctx)) // guard was released
	g.Unlock()

	// Nested: the inner panic is wrapped once; the outer passes it through.
	other, err := Open(slog.New(slog.DiscardHandler), s.T().TempDir())
	s.Require().NoError(err)
	defer other.Close()
	g2 := NewWriteGuard()
	g3 := NewWriteGuard()
	got = recoverTx(func() {
		_ = g2.WithTx(ctx, database, func(*sql.Tx) error {
			return g3.WithTx(ctx, other, func(*sql.Tx) error { panic(http.ErrAbortHandler) })
		})
	})
	outer, ok := got.(*TxPanic)
	s.Require().True(ok)
	s.Equal(http.ErrAbortHandler, outer.Value)
	s.Require().ErrorIs(outer, http.ErrAbortHandler)
}

func asError(v any) error {
	e, _ := v.(error)

	return e
}
