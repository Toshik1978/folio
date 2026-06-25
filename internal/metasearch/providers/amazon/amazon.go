// Package amazon is a metasearch CoverSource that fetches a book's cover from
// its Amazon product page, keyed by ASIN. The product page carries the real
// high-resolution print cover; Amazon's search results only expose a squared
// thumbnail, so this source contributes nothing without an ASIN and lets the
// ISBN/title sources handle those books.
package amazon

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.amazon.com"
	maxHTMLBytes   = 4 << 20
	politeInterval = time.Second
	maxRedirects   = 5
	// imgTag is the HTML img element name.
	imgTag = "img"
	// thumbHeight is the uniform pixel height requested for the picker thumbnail,
	// large enough to render crisply.
	thumbHeight = 450
)

// Source fetches Amazon product-page covers.
type Source struct {
	baseURL string
	client  *http.Client
	limiter *rateLimiter
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	s := &Source{
		baseURL: defaultBaseURL,
		limiter: newRateLimiter(politeInterval),
	}
	s.client = &http.Client{Timeout: timeout, CheckRedirect: s.checkRedirect}

	return s
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceAmazon }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers returns the cover from the book's Amazon product page, keyed by
// ASIN. Without an ASIN there is nothing reliable to fetch, so it returns no
// candidates rather than scraping noisy search-result thumbnails.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	if q.ASIN == "" {
		return nil, nil
	}
	covers, err := s.fetchProductCover(ctx, q.ASIN)
	if err != nil {
		return nil, fmt.Errorf("amazon product: %w", err)
	}

	return covers, nil
}

// checkRedirect bounds redirect depth on the product fetch.
func (s *Source) checkRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("amazon: stopped after %d redirects", maxRedirects)
	}

	return nil
}
