// Package amazon is a metasearch CoverSource that scrapes Amazon search-result
// thumbnails, with a DuckDuckGo fallback when Amazon serves an anti-bot
// interstitial. Scraping is accepted here: a private personal server, a handful
// of one-off lookups, and a maintainer who fixes the parser when markup drifts.
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
	maxAttempts    = 3
	retryBackoff   = 400 * time.Millisecond
)

// Source scrapes Amazon for cover candidates.
type Source struct {
	baseURL string
	client  *http.Client
	backoff time.Duration
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{
		baseURL: defaultBaseURL,
		client:  &http.Client{Timeout: timeout},
		backoff: retryBackoff,
	}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceAmazon }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers runs the direct Amazon scrape. (Task 4 adds the DDG fallback.)
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	out, err := s.searchDirect(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("amazon search: %w", err)
	}

	return out, nil
}
