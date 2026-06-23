package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
)

// statsSuite tests concurrent cold-cache serialization for the /stats endpoint.
type statsSuite struct {
	baseSuite
}

// TestStatsConcurrentColdCache fires n concurrent cold-cache /stats requests and
// asserts that computeStats is called exactly once. A sync barrier (WaitGroup +
// gate channel) ensures all goroutines reach the handler before any completes.
func (s *statsSuite) TestStatsConcurrentColdCache() {
	const n = 8

	src := s.seedLibrary("folder", "/lib")
	s.seedBook(src, bookSeed{Title: "A", Format: "epub", Size: 100})

	// Instrument the handler with a call counter.
	var (
		hookMu sync.Mutex
		calls  int
	)
	s.catalog.computeHook = func() {
		hookMu.Lock()
		calls++
		hookMu.Unlock()
	}
	defer func() { s.catalog.computeHook = nil }()

	// Gate channel: all goroutines block here until we close it, so they all
	// hit the handler at effectively the same instant (cold cache).
	gate := make(chan struct{})

	type result struct{ code int }
	results := make([]result, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			<-gate // wait for the starting gun

			req := httptest.NewRequestWithContext(
				context.Background(), http.MethodGet, "/stats", http.NoBody)
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)
			results[idx] = result{code: w.Code}
		}(i)
	}

	close(gate) // release all goroutines simultaneously
	wg.Wait()

	for i, r := range results {
		s.Equal(http.StatusOK, r.code, "goroutine %d got unexpected status", i)
	}

	hookMu.Lock()
	got := calls
	hookMu.Unlock()

	s.Equal(1, got, "computeStats must be called exactly once under %d concurrent cold-cache requests", n)
}

func TestStatsConcurrency(t *testing.T) {
	suite.Run(t, new(statsSuite))
}
