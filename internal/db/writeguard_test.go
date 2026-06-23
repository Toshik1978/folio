package db

import (
	"context"
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
