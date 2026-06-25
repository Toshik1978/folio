package amazon

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/html"

	"github.com/Toshik1978/folio/internal/metasearch"
)

// interstitialMarkers are substrings that appear on Amazon's CAPTCHA/robot
// pages (served with HTTP 200), used to distinguish a block from a real page.
var interstitialMarkers = []string{ //nolint:gochecknoglobals // immutable lookup table
	"automated access",
	"enter the characters",
	"api-services-support",
	"something went wrong on our end",
	"bm-verify",
	"triggerinterstitialchallenge",
}

// isInterstitial reports whether body looks like an Amazon anti-bot page.
func isInterstitial(body []byte) bool {
	low := bytes.ToLower(body)
	for _, m := range interstitialMarkers {
		if bytes.Contains(low, []byte(m)) {
			return true
		}
	}

	return false
}

// fetchProductCover fetches an Amazon product page by ASIN and returns its main
// cover — the high-resolution portrait image Amazon shows on the detail page
// (e.g. _SL1500_), which is the actual print cover rather than the squared
// search-result thumbnail. It returns an empty slice (no error) when the page
// has no recognizable main image, so the aggregator simply gets no Amazon
// candidate for this book.
func (s *Source) fetchProductCover(ctx context.Context, asin string) ([]metasearch.CoverCandidate, error) {
	if err := s.limiter.wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, s.baseURL+"/dp/"+url.PathEscape(asin), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build product request: %w", err)
	}
	req.Header.Set("User-Agent", metasearch.RandomUserAgent())
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", acceptHTML)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amazon product request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amazon product status %d: %w", resp.StatusCode, metasearch.ErrBlocked)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLBytes))
	if err != nil {
		return nil, fmt.Errorf("read product body: %w", err)
	}
	if isInterstitial(body) {
		return nil, fmt.Errorf("amazon product interstitial: %w",
			errors.Join(metasearch.ErrBlocked, metasearch.ErrNoRetry))
	}

	cover := productCoverURL(body)
	if cover == "" {
		return nil, nil
	}

	return []metasearch.CoverCandidate{{
		Source:   metasearch.SourceAmazon,
		ThumbURL: metasearch.ThumbCDNImage(cover, thumbHeight),
		FullURL:  metasearch.OriginalCDNImage(cover),
	}}, nil
}

// productCoverURL extracts the main cover image URL from an Amazon product page.
// It prefers the explicit high-res attribute (data-old-hires) and otherwise
// picks the largest entry from the responsive image map (data-a-dynamic-image).
func productCoverURL(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	img := findNode(doc, func(n *html.Node) bool {
		return n.Data == imgTag && attr(n, "data-a-dynamic-image") != ""
	})
	if img == nil {
		return ""
	}
	if hires := strings.TrimSpace(attr(img, "data-old-hires")); hires != "" {
		return hires
	}

	return largestDynamicImage(attr(img, "data-a-dynamic-image"))
}

// largestDynamicImage parses Amazon's data-a-dynamic-image JSON (a map of image
// URL to [width, height]) and returns the URL with the largest pixel area. Ties
// break on URL for determinism.
func largestDynamicImage(jsonAttr string) string {
	var dims map[string][]int
	if json.Unmarshal([]byte(jsonAttr), &dims) != nil {
		return ""
	}
	type entry struct {
		url  string
		area int
	}
	entries := make([]entry, 0, len(dims))
	for u, wh := range dims {
		area := 0
		if len(wh) == 2 {
			area = wh[0] * wh[1]
		}
		entries = append(entries, entry{url: u, area: area})
	}
	if len(entries) == 0 {
		return ""
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if a.area != b.area {
			return cmp.Compare(b.area, a.area) // larger area first
		}

		return cmp.Compare(a.url, b.url)
	})

	return entries[0].url
}
