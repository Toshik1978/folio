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
	baseURL string
	http    *http.Client
}

// New builds an Open Library cover source with the given per-request timeout.
func New(timeout time.Duration) *Source {
	return &Source{baseURL: defaultBaseURL, http: &http.Client{Timeout: timeout}}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceOpenLibrary }

// Capabilities reports that this source supplies covers.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// searchDoc is a single document entry from the Open Library search API response.
type searchDoc struct {
	Title  string `json:"title"`
	CoverI int    `json:"cover_i"`
}

type searchResponse struct {
	Docs []searchDoc `json:"docs"`
}

// searchParams builds the Open Library query parameters for q. ISBN is an
// exact key and is searched alone; title/author are only used when no ISBN is
// available (adding a mismatched title/author would zero the result).
func searchParams(q metasearch.Query) url.Values {
	params := url.Values{}
	if q.ISBN != "" {
		params.Set("isbn", q.ISBN)
	} else {
		if q.Title != "" {
			params.Set("title", q.Title)
		}
		if q.Author != "" {
			params.Set("author", q.Author)
		}
	}
	params.Set("limit", strconv.Itoa(maxDocs))

	return params
}

// toCandidates maps the search response docs to cover candidates. When byISBN
// is true the candidate Title is left empty so the aggregator's relevance
// filter fails open (the ISBN already pins the exact edition).
func toCandidates(docs []searchDoc, byISBN bool) []metasearch.CoverCandidate {
	out := make([]metasearch.CoverCandidate, 0, len(docs))
	for _, d := range docs {
		if d.CoverI == 0 {
			continue
		}
		title := d.Title
		if byISBN {
			title = "" // exact key: fail open, never title-filter
		}
		out = append(out, metasearch.CoverCandidate{
			Source:   metasearch.SourceOpenLibrary,
			Title:    title,
			ThumbURL: fmt.Sprintf("%s/%d-M.jpg", coversBase, d.CoverI),
			FullURL:  fmt.Sprintf("%s/%d-L.jpg", coversBase, d.CoverI),
		})
	}

	return out
}

// SearchCovers runs a title/author search and maps docs that carry a cover id.
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	params := searchParams(q)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/search.json?"+params.Encode(), http.NoBody)
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

	return toCandidates(out.Docs, q.ISBN != ""), nil
}
