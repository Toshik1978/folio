// Package openlibrary is a metasearch CoverSource backed by the Open Library
// search API. It is a clean REST source (no scraping): search.json yields cover
// ids, which map to covers.openlibrary.org image URLs.
package openlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Toshik1978/folio/internal/metasearch"
)

const (
	defaultBaseURL = "https://openlibrary.org"
	coversBase     = "https://covers.openlibrary.org/b/id"
	maxJSONBytes   = 10 << 20
	maxDocs        = 12 // cap candidates so the grid stays manageable
)

// Source queries Open Library for cover candidates.
type Source struct {
	BaseURL string
	http    *http.Client
}

// New builds an Open Library cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{BaseURL: defaultBaseURL, http: &http.Client{Timeout: timeout}}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceOpenLibrary }

// Capabilities reports that this source supplies covers.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

type searchResponse struct {
	Docs []struct {
		Title  string `json:"title"`
		CoverI int    `json:"cover_i"`
	} `json:"docs"`
}

// SearchCovers runs a title/author search and maps docs that carry a cover id.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	params := url.Values{}
	if q.Title != "" {
		params.Set("title", q.Title)
	}
	if q.Author != "" {
		params.Set("author", q.Author)
	}
	if q.ISBN != "" {
		params.Set("isbn", q.ISBN)
	}
	params.Set("limit", strconv.Itoa(maxDocs))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/search.json?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openlibrary request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openlibrary status %d", resp.StatusCode)
	}

	var out searchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	candidates := make([]metasearch.CoverCandidate, 0, len(out.Docs))
	for _, d := range out.Docs {
		if d.CoverI == 0 {
			continue
		}
		candidates = append(candidates, metasearch.CoverCandidate{
			Source:   metasearch.SourceOpenLibrary,
			ThumbURL: fmt.Sprintf("%s/%d-M.jpg", coversBase, d.CoverI),
			FullURL:  fmt.Sprintf("%s/%d-L.jpg", coversBase, d.CoverI),
		})
	}

	return candidates, nil
}
