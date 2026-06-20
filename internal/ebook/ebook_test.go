package ebook

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TestEbook is the package's single entry point; every suite is registered here.
func TestEbook(t *testing.T) {
	suite.Run(t, new(epubSuite))
	suite.Run(t, new(fb2Suite))
	suite.Run(t, new(mobiSuite))
	suite.Run(t, new(pdfSuite))
	suite.Run(t, new(dispatchSuite))
	suite.Run(t, new(metadataSuite))
}

// baseSuite resolves fixture paths under testdata/.
type baseSuite struct {
	suite.Suite

	log *slog.Logger
	d   *Dispatcher
}

func (s *baseSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.d = NewDispatcher(NewEPUB(), NewFB2(), NewMOBI(), NewPDF())
}

func (s *baseSuite) fixture(name string) string {
	return filepath.Join("testdata", name)
}

// parsePeakHeapBytes parses path while sampling the live heap, returning the
// metadata and the peak heap growth observed during the parse. Peak live heap —
// not cumulative allocation — is what the OOM killer measures, so this is the
// metric that reflects the container crash: loading a file whole keeps one large
// buffer live for the entire parse, whereas streaming never does. The signal we
// assert on (tens of MB live vs. a few KB) dwarfs sampling jitter.
func (s *baseSuite) parsePeakHeapBytes(path string) (Metadata, uint64) {
	runtime.GC() //revive:disable:call-to-gc // settle the heap for a clean peak-growth baseline
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			for {
				cur := peak.Load()
				if ms.HeapAlloc <= cur || peak.CompareAndSwap(cur, ms.HeapAlloc) {
					break
				}
			}
			select {
			case <-done:
				return
			case <-time.After(100 * time.Microsecond):
			}
		}
	}()

	m, err := s.d.Parse(context.Background(), s.log, path)
	close(done)
	<-stopped
	s.Require().NoError(err)

	if p := peak.Load(); p > base.HeapAlloc {
		return m, p - base.HeapAlloc
	}

	return m, 0
}
