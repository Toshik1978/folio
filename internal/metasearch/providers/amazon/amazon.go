// Package amazon is a metasearch CoverSource that scrapes Amazon search-result
// thumbnails, with a DuckDuckGo fallback when Amazon serves an anti-bot
// interstitial. Scraping is accepted here: a private personal server, a handful
// of one-off lookups, and a maintainer who fixes the parser when markup drifts.
package amazon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.amazon.com"
	defaultDDGURL  = "https://html.duckduckgo.com"
	maxHTMLBytes   = 4 << 20
	maxAttempts    = 3
	retryBackoff   = 400 * time.Millisecond
	politeInterval = time.Second
)

// Source scrapes Amazon for cover candidates, with a DuckDuckGo fallback.
type Source struct {
	baseURL string
	ddgURL  string
	client  *http.Client
	backoff time.Duration
	limiter *rateLimiter
	// allowProductHost gates which DDG result hosts may be fetched (defense in
	// depth, since URLs come from an external source). Tests override it.
	allowProductHost func(rawURL string) bool
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{
		baseURL:          defaultBaseURL,
		ddgURL:           defaultDDGURL,
		client:           &http.Client{Timeout: timeout},
		backoff:          retryBackoff,
		limiter:          &rateLimiter{interval: politeInterval},
		allowProductHost: isAmazonHost,
	}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceAmazon }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers tries the direct Amazon scrape and, when Amazon blocks it, falls
// back to a DuckDuckGo product-page lookup.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := s.searchDirect(ctx, q)
	if err == nil && len(out) > 0 {
		return out, nil
	}
	if !errors.Is(err, metasearch.ErrBlocked) {
		// A non-block error (or genuine nil/empty): nothing the fallback fixes.
		if err != nil {
			return nil, fmt.Errorf("amazon search: %w", err)
		}

		return out, nil
	}

	fb, ferr := s.searchFallback(ctx, q)
	if ferr == nil && len(fb) > 0 {
		return fb, nil
	}
	// Fallback found nothing: report the original block so the aggregator logs it.
	return nil, fmt.Errorf("amazon search: %w", metasearch.ErrBlocked)
}
