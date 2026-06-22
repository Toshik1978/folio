// Package amazon is a metasearch CoverSource that scrapes Amazon search-result
// thumbnails. Scraping is accepted here: a private personal server, a handful of
// one-off lookups, and a maintainer who fixes the parser when markup drifts —
// drift is caught by the golden-HTML parser test, not by a silent empty grid.
package amazon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://www.amazon.com"
	// userAgent mimics a real browser so Amazon serves the standard result markup.
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36" +
		" (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	maxHTMLBytes = 4 << 20
	imageClass   = "s-image"
)

// Source scrapes Amazon for cover candidates.
type Source struct {
	BaseURL string
	client  *http.Client
}

// New builds an Amazon cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{BaseURL: defaultBaseURL, client: &http.Client{Timeout: timeout}}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceAmazon }

// Capabilities reports cover support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers fetches the Amazon books search page for the query and parses the
// result thumbnails.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	params := url.Values{}
	params.Set("k", strings.TrimSpace(q.Title+" "+q.Author))
	params.Set("i", "stripbooks")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/s?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amazon request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amazon status %d", resp.StatusCode)
	}

	return parseCovers(io.LimitReader(resp.Body, maxHTMLBytes))
}

// parseCovers extracts cover candidates from an Amazon search-results document.
// It collects <img class="s-image"> nodes and picks the highest-density srcset
// entry (falling back to src) as the full URL.
func parseCovers(r io.Reader) ([]metasearch.CoverCandidate, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	var out []metasearch.CoverCandidate
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" && hasClass(n, imageClass) {
			if full := bestImage(n); full != "" {
				out = append(out, metasearch.CoverCandidate{
					Source:   metasearch.SourceAmazon,
					ThumbURL: attr(n, "src"),
					FullURL:  metasearch.OriginalAmazonImage(full),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return out, nil
}

// bestImage returns the highest-density srcset URL, or src when there is no srcset.
func bestImage(n *html.Node) string {
	if best := highestDensity(attr(n, "srcset")); best != "" {
		return best
	}

	return attr(n, "src")
}

// highestDensity parses a srcset and returns the URL with the largest density
// descriptor (e.g. "4x").
func highestDensity(srcset string) string {
	var bestURL string
	var bestD float64
	for part := range strings.SplitSeq(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		d := 1.0
		if len(fields) > 1 {
			d, _ = strconv.ParseFloat(strings.TrimSuffix(fields[1], "x"), 64)
		}
		if d >= bestD {
			bestD, bestURL = d, fields[0]
		}
	}

	return bestURL
}

// hasClass reports whether n's class attribute contains the given class token.
func hasClass(n *html.Node, class string) bool {
	return slices.Contains(strings.Fields(attr(n, "class")), class)
}

// attr returns n's value for the named attribute, or "".
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}

	return ""
}
