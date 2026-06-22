package db

import (
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
			g.Lock()
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
