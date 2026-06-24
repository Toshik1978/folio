package amazon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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
	suite.Run(t, new(ddgSuite))
}

// baseSuite holds helpers shared by the parser and HTTP suites.
type baseSuite struct {
	suite.Suite
}

// fixture returns the golden Amazon search-result HTML.
func (s *baseSuite) fixture() []byte {
	data, err := os.ReadFile("testdata/search.html")
	s.Require().NoError(err)

	return data
}

// sourceForHandler starts an httptest server with h (cleaned up automatically)
// and returns a Source wired to it with a near-zero retry backoff so retry tests
// don't wait the full 400 ms.
func (s *baseSuite) sourceForHandler(h http.HandlerFunc) *Source {
	srv := httptest.NewServer(h)
	s.T().Cleanup(srv.Close)

	src := New(5 * time.Second)
	src.baseURL = srv.URL
	src.backoff = time.Millisecond

	return src
}

// parseSuite covers the offline HTML parser and capability reporting.
type parseSuite struct {
	baseSuite
}

func (s *parseSuite) TestParseCoversFromFixture() {
	f, err := os.Open("testdata/search.html")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	got, err := parseCovers(f)
	s.Require().NoError(err)

	// Two s-image results; the sprite (other-image) is ignored.
	s.Require().Len(got, 2)
	for _, c := range got {
		s.Equal(metasearch.SourceAmazon, c.Source)
		s.Contains(c.FullURL, "https://m.media-amazon.com/images/I/")
	}
	// The highest-density srcset entry is chosen and the size modifier is stripped to get the original.
	s.Equal("https://m.media-amazon.com/images/I/aaa.jpg", got[0].FullURL)
	s.Equal("https://m.media-amazon.com/images/I/bbb.jpg", got[1].FullURL)
}

func (s *parseSuite) TestPickSrcsetSkipsMalformedAndPicksHighest() {
	// Malformed descriptor before any valid entry must be skipped; highest valid wins.
	srcset := "https://img/bad.jpg garbage, https://img/lo.jpg 1x, https://img/hi.jpg 3x"
	s.Equal("https://img/hi.jpg", highestDensity(srcset))

	// A srcset that contains only a malformed entry should return "".
	s.Empty(highestDensity("https://img/bad.jpg garbage"))

	// Equal densities: first entry wins (strict > keeps the earlier one).
	s.Equal("https://img/first.jpg", highestDensity("https://img/first.jpg 2x, https://img/second.jpg 2x"))
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

func (s *searchSuite) TestSearchCovers() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	for _, c := range got {
		s.Equal(metasearch.SourceAmazon, c.Source)
	}
}

func (s *searchSuite) TestSearchCoversNon200IsBlocked() {
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestSearchCoversCaptchaIsBlocked() {
	captcha, err := os.ReadFile("testdata/captcha.html")
	s.Require().NoError(err)
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(captcha) // HTTP 200 with a CAPTCHA body
	})

	_, err = src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().Error(err)
	s.Require().ErrorIs(err, metasearch.ErrBlocked)
}

func (s *searchSuite) TestSearchCoversRetriesOnTransientBlock() {
	var reqCount atomic.Int32
	src := s.sourceForHandler(func(w http.ResponseWriter, _ *http.Request) {
		n := reqCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}
		_, _ = w.Write(s.fixture())
	})

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().NotEmpty(got)
	s.GreaterOrEqual(int(reqCount.Load()), 2, "server should have been hit at least twice")
}

// ddgSuite covers DuckDuckGo fallback logic.
type ddgSuite struct {
	baseSuite
}

func (s *ddgSuite) TestParseDDGResultsKeepsOnlyDPLinks() {
	f, err := os.Open("testdata/ddg.html")
	s.Require().NoError(err)
	defer func() { _ = f.Close() }()

	got := parseDDGResults(f)
	s.Require().Len(got, 2, "two /dp/ links (amazon + example.com); the /gp/ link is dropped")
	s.Equal("https://www.amazon.com/Book-Title/dp/B01ABCDEFG/ref=sr", got[0])
	s.Contains(got[1], "example.com")
}

func (s *ddgSuite) TestIsAmazonHost() {
	s.True(isAmazonHost("https://www.amazon.com/x/dp/B01"))
	s.True(isAmazonHost("https://www.amazon.co.uk/dp/B01"))
	s.False(isAmazonHost("https://example.com/dp/B01"))
	s.False(isAmazonHost("https://notamazon.evil.com/dp/B01"))
}

func (s *ddgSuite) TestRateLimiterEnforcesInterval() {
	rl := &rateLimiter{interval: 40 * time.Millisecond}
	start := time.Now()
	s.Require().NoError(rl.wait(context.Background())) // first: no wait
	s.Require().NoError(rl.wait(context.Background())) // second: waits ~interval
	s.GreaterOrEqual(time.Since(start), 40*time.Millisecond)
}

// TestFallbackOnDirectBlock: direct server returns a CAPTCHA, DDG server returns
// a result linking to a product page on the same server, and the product page
// yields an og:image cover.
func (s *ddgSuite) TestFallbackOnDirectBlock() {
	captcha, err := os.ReadFile("testdata/captcha.html")
	s.Require().NoError(err)
	product, err := os.ReadFile("testdata/product.html")
	s.Require().NoError(err)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(captcha)
	}))
	s.T().Cleanup(direct.Close)

	mux := http.NewServeMux()
	var ddg *httptest.Server
	mux.HandleFunc("/html/", func(w http.ResponseWriter, _ *http.Request) {
		// Link straight to the product page on this same server (no uddg wrapper).
		_, _ = w.Write([]byte(`<a class="result__a" href="` + ddg.URL + `/x/dp/B01ABCDEFG">hit</a>`))
	})
	mux.HandleFunc("/x/dp/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(product)
	})
	ddg = httptest.NewServer(mux)
	s.T().Cleanup(ddg.Close)

	src := New(5 * time.Second)
	src.baseURL = direct.URL
	src.backoff = time.Millisecond
	src.ddgURL = ddg.URL
	src.limiter = &rateLimiter{interval: 0}
	src.allowProductHost = func(string) bool { return true } // allow the 127.0.0.1 test host

	got, err := src.SearchCovers(context.Background(), metasearch.Query{Title: "Dune"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Equal(metasearch.SourceAmazon, got[0].Source)
	// og:image is preferred and the _AC_SL1500_ modifier is stripped.
	s.Equal("https://m.media-amazon.com/images/I/zzz.jpg", got[0].FullURL)
}

// TestProductImagePrecedence verifies the three-level precedence:
// og:image > data-old-hires > src.
func (s *ddgSuite) TestProductImagePrecedence() {
	// (a) no og:image + landingImage with both data-old-hires and src → data-old-hires wins.
	htmlA := `<!doctype html><html><body>
<img id="landingImage" data-old-hires="https://example.com/hires.jpg" src="https://example.com/low.jpg">
</body></html>`
	s.Equal("https://example.com/hires.jpg", productImage(bytes.NewBufferString(htmlA)),
		"data-old-hires should win when og:image absent")

	// (b) no og:image + landingImage with only src → src wins.
	htmlB := `<!doctype html><html><body>
<img id="landingImage" src="https://example.com/low.jpg">
</body></html>`
	s.Equal("https://example.com/low.jpg", productImage(bytes.NewBufferString(htmlB)),
		"src should be used when og:image and data-old-hires are absent")

	// (c) og:image present → og:image wins even if landingImage exists.
	htmlC := `<!doctype html><html><head>
<meta property="og:image" content="https://example.com/og.jpg" />
</head><body>
<img id="landingImage" data-old-hires="https://example.com/hires.jpg" src="https://example.com/low.jpg">
</body></html>`
	s.Equal("https://example.com/og.jpg", productImage(bytes.NewBufferString(htmlC)),
		"og:image should win over data-old-hires and src")
}
