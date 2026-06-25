package amazon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// TestAmazon is the package's single entry point; every suite is registered here.
func TestAmazon(t *testing.T) {
	suite.Run(t, new(parseSuite))
	suite.Run(t, new(searchSuite))
}

// baseSuite holds helpers shared by the parser and HTTP suites.
type baseSuite struct {
	suite.Suite
}

// sourceForHandler starts an httptest server with h (cleaned up automatically)
// and returns a Source wired to it with a zero rate-limit interval for speed.
func (s *baseSuite) sourceForHandler(h http.HandlerFunc) *Source {
	srv := httptest.NewServer(h)
	s.T().Cleanup(srv.Close)

	src := New(5 * time.Second)
	src.baseURL = srv.URL
	src.limiter = newRateLimiter(0)
	src.backoff = time.Millisecond

	return src
}

// parseSuite covers the offline product-page cover extraction.
type parseSuite struct {
	baseSuite
}

func (s *parseSuite) TestProductCoverURLPrefersHiRes() {
	body := []byte(`<!doctype html><html><body>
<img id="landingImage" class="a-dynamic-image"
  data-old-hires="https://m.media-amazon.com/images/I/81G3FEapceL._SL1500_.jpg"
  data-a-dynamic-image="{&quot;https://m.media-amazon.com/images/I/81G3FEapceL._SY342_.jpg&quot;:[230,342]}">
</body></html>`)
	s.Equal("https://m.media-amazon.com/images/I/81G3FEapceL._SL1500_.jpg", productCoverURL(body))
}

func (s *parseSuite) TestProductCoverURLFallsBackToLargestDynamic() {
	body := []byte(`<!doctype html><html><body>
<img class="a-dynamic-image"
  data-a-dynamic-image="{&quot;https://img/a._SY200_.jpg&quot;:[130,200],&quot;https://img/a._SY500_.jpg&quot;:[325,500]}">
</body></html>`)
	s.Equal("https://img/a._SY500_.jpg", productCoverURL(body))
}

func (s *parseSuite) TestProductCoverURLEmptyWhenNoImage() {
	s.Empty(productCoverURL([]byte(`<html><body><p>no image</p></body></html>`)))
}

func (s *parseSuite) TestIsInterstitial() {
	block := []byte(`<html><body>Enter the characters you see below</body></html>`)
	s.True(isInterstitial(block))
	// A benign page that merely quotes "something went wrong" in a review is not a block.
	s.False(isInterstitial([]byte(`<p>a tale where something went wrong for the hero</p>`)))
}

func (s *parseSuite) TestCapabilities() {
	src := New(time.Second)
	s.Equal(metasearch.SourceAmazon, src.Name())
	s.True(metasearch.HasCapability(src.Capabilities(), metasearch.CapCover))
}

// searchSuite drives SearchCovers end-to-end over HTTP.
type searchSuite struct {
	baseSuite
}

func (s *searchSuite) TestSearchByASINUsesProductPage() {
	var paths []string
	src := s.sourceForHandler(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<img id="landingImage" class="a-dynamic-image"
  data-old-hires="https://m.media-amazon.com/images/I/81G3FEapceL._SL1500_.jpg"
  data-a-dynamic-image="{&quot;https://m.media-amazon.com/images/I/81G3FEapceL._SY342_.jpg&quot;:[230,342]}">
</body></html>`))
	})

	got, err := src.SearchCovers(context.Background(),
		metasearch.Query{Title: "Death's End", ASIN: "B00WDVKZY0"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	// The product page's high-res image, stripped to its full-resolution URL,
	// with a uniform thumbnail.
	s.Equal("https://m.media-amazon.com/images/I/81G3FEapceL.jpg", got[0].FullURL)
	s.Equal("https://m.media-amazon.com/images/I/81G3FEapceL._SY450_.jpg", got[0].ThumbURL)
	s.Require().Len(paths, 1)
	s.Contains(paths[0], "/dp/B00WDVKZY0")
}

func (s *searchSuite) TestSearchWithoutASINReturnsNothing() {
	hit := false
	src := s.sourceForHandler(func(http.ResponseWriter, *http.Request) { hit = true })

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Death's End"})
	s.Require().NoError(err)
	s.Empty(got)
	s.False(hit, "no ASIN must not issue any request")
}

func (s *searchSuite) TestProductPageNon200IsBlocked() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{ASIN: "B00WDVKZY0"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestProductPageInterstitialIsBlocked() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>Enter the characters you see below</body></html>`))
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{ASIN: "B00WDVKZY0"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestProductFetchIsThrottled() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<img class="a-dynamic-image" data-old-hires="https://img/a._SL1500_.jpg">`))
	})
	src.limiter = newRateLimiter(60 * time.Millisecond)

	start := time.Now()
	_, err := src.SearchCovers(context.Background(), metasearch.Query{ASIN: "A1"})
	s.Require().NoError(err)
	_, err = src.SearchCovers(context.Background(), metasearch.Query{ASIN: "A2"})
	s.Require().NoError(err)
	elapsed := time.Since(start)
	s.GreaterOrEqual(elapsed, 60*time.Millisecond, "two fetches must be spaced by one interval")
	s.Less(elapsed, 5*time.Second, "throttle must not hang or stack extra intervals")
}

func (s *searchSuite) TestRateLimiterEnforcesInterval() {
	rl := newRateLimiter(40 * time.Millisecond)
	start := time.Now()
	s.Require().NoError(rl.wait(context.Background())) // first: no wait
	s.Require().NoError(rl.wait(context.Background())) // second: waits ~interval
	s.GreaterOrEqual(time.Since(start), 40*time.Millisecond)
}

func (s *searchSuite) TestCheckRedirectBoundsDepth() {
	src := New(time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://www.amazon.com/x/dp/B01", http.NoBody)
	s.Require().NoError(err)

	s.Require().NoError(src.checkRedirect(req, nil))
	via := make([]*http.Request, maxRedirects)
	s.Require().Error(src.checkRedirect(req, via))
}

func (s *searchSuite) TestSearchRetriesTransientBlock() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		if reqCount.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		_, _ = w.Write(
			[]byte(
				`<img class="a-dynamic-image" data-old-hires="https://img/a._SL1500_.jpg" data-a-dynamic-image="{&quot;https://img/a._SY342_.jpg&quot;:[230,342]}">`,
			),
		)
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{ASIN: "B00WDVKZY0"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	s.GreaterOrEqual(int(reqCount.Load()), 2, "a transient 503 must be retried")
}

func (s *searchSuite) TestSearchDoesNotRetryInterstitial() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		reqCount.Add(1)
		_, _ = w.Write([]byte(`<html><body>Enter the characters you see below</body></html>`))
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{ASIN: "B00WDVKZY0"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
	s.Equal(int32(1), reqCount.Load(), "an interstitial is terminal (ErrNoRetry): no retry")
}
