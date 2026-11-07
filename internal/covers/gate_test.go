package covers

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stretchr/testify/suite"
)

// blockingExtractor holds every Cover call until release is closed (or the
// call's context ends), tracking how many calls are inside Cover at once so a
// test can observe the extraction cap.
type blockingExtractor struct {
	data    []byte
	release chan struct{}

	mu     sync.Mutex
	active int
	peak   int
	calls  int
}

func newBlockingExtractor(data []byte) *blockingExtractor {
	return &blockingExtractor{data: data, release: make(chan struct{})}
}

func (b *blockingExtractor) Cover(ctx context.Context, _ int64) ([]byte, bool, error) {
	b.mu.Lock()
	b.calls++
	b.active++
	b.peak = max(b.peak, b.active)
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()

	select {
	case <-b.release:
		return b.data, true, nil
	case <-ctx.Done():
		return nil, false, fmt.Errorf("blocked cover: %w", ctx.Err())
	}
}

func (b *blockingExtractor) snapshot() (active, peak, calls int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.active, b.peak, b.calls
}

// extractGateTestSuite covers the bound on concurrent cold cover extraction: a
// page of uncached thumbnails must not decode every cover at once.
type extractGateTestSuite struct {
	suite.Suite

	dataDir string
	ext     *blockingExtractor
	store   *Store
}

func (s *extractGateTestSuite) SetupTest() {
	s.dataDir = s.T().TempDir()
	var buf bytes.Buffer
	s.Require().NoError(jpeg.Encode(&buf, sampleImage(), nil))
	s.ext = newBlockingExtractor(buf.Bytes())
	store, err := NewStore(s.dataDir, s.ext, newFakeState())
	s.Require().NoError(err)
	s.store = store
}

// serveThumbs requests the thumbnails of books first..first+n-1 concurrently,
// returning a WaitGroup that completes once every request has been answered.
func (s *extractGateTestSuite) serveThumbs(ctx context.Context, first, n int64) *sync.WaitGroup {
	var wg sync.WaitGroup
	for id := first; id < first+n; id++ {
		wg.Go(func() {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
			s.store.ServeThumbnail(httptest.NewRecorder(), req, id)
		})
	}

	return &wg
}

// fillGate starts maxColdExtractions blocked extractions and waits until all of
// them are inside the extractor, so the gate is full.
func (s *extractGateTestSuite) fillGate() *sync.WaitGroup {
	wg := s.serveThumbs(context.Background(), 1, maxColdExtractions)
	s.Require().Eventually(func() bool {
		active, _, _ := s.ext.snapshot()
		return active == maxColdExtractions
	}, 2*time.Second, 5*time.Millisecond)

	return wg
}

func (s *extractGateTestSuite) TestColdThumbnailsExtractAtMostCapAtOnce() {
	const requests = 3 * maxColdExtractions
	wg := s.serveThumbs(context.Background(), 1, requests)

	s.Require().Eventually(func() bool {
		active, _, _ := s.ext.snapshot()
		return active == maxColdExtractions
	}, 2*time.Second, 5*time.Millisecond)
	// Give the queued requests a chance to slip past the cap if it is broken.
	time.Sleep(50 * time.Millisecond)
	_, peak, _ := s.ext.snapshot()
	s.Equal(maxColdExtractions, peak, "no more than the cap may extract at once")

	close(s.ext.release)
	wg.Wait()
	_, peak, calls := s.ext.snapshot()
	s.Equal(maxColdExtractions, peak)
	s.Equal(requests, calls, "every queued request is eventually served")
}

func (s *extractGateTestSuite) TestCancelledWaiterGivesUpWithoutExtracting() {
	held := s.fillGate()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
		s.store.ServeThumbnail(rec, req, 100)

		done <- rec
	}()
	cancel()

	select {
	case rec := <-done:
		s.Equal(placeholderJPEG, rec.Body.Bytes(), "a cancelled waiter gets the placeholder")
		s.Equal("no-cache", rec.Header().Get("Cache-Control"))
	case <-time.After(2 * time.Second):
		s.Fail("a cancelled request must stop waiting for the gate")
	}
	_, _, calls := s.ext.snapshot()
	s.Equal(maxColdExtractions, calls, "the cancelled request never reached the extractor")
	_, err := os.Stat(s.store.Path(100))
	s.True(os.IsNotExist(err), "nothing is cached for the cancelled request")

	close(s.ext.release)
	held.Wait()
}

func (s *extractGateTestSuite) TestCachedFilesBypassTheGate() {
	held := s.fillGate()

	thumb := filepath.Clean(s.store.ThumbPath(200))
	s.Require().NoError(os.MkdirAll(filepath.Dir(thumb), 0o755))
	s.Require().NoError(os.WriteFile(thumb, s.ext.data, 0o600))
	s.Require().NoError(os.WriteFile(s.store.Path(201), s.ext.data, 0o600))

	served := make(chan struct{})
	go func() {
		defer close(served)
		rec := httptest.NewRecorder()
		s.store.ServeThumbnail(
			rec,
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody),
			200,
		)
		s.Equal(http.StatusOK, rec.Code)
		s.Equal(s.ext.data, rec.Body.Bytes())

		rec = httptest.NewRecorder()
		s.store.ServeCover(
			rec,
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody),
			201,
		)
		s.Equal(http.StatusOK, rec.Code)
		s.Equal(s.ext.data, rec.Body.Bytes())
	}()

	select {
	case <-served:
	case <-time.After(2 * time.Second):
		s.Fail("cached thumbnails and covers must not wait for cold extractions")
	}

	close(s.ext.release)
	held.Wait()
}

func (s *extractGateTestSuite) TestHasLocalCoverProbeHonoursTheGate() {
	held := s.fillGate()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Undetermined within the budget: conservatively "a cover may exist".
	s.True(s.store.HasLocalCover(ctx, 300))
	_, _, calls := s.ext.snapshot()
	s.Equal(maxColdExtractions, calls, "the probe waited for the gate instead of extracting")

	close(s.ext.release)
	held.Wait()
}
