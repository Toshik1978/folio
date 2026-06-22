// Package googlebooks adapts the low-level googlebooks.Client to the metasearch
// provider model. In Phase 2 it is a cover-only Source; Phase 3 promotes it to
// also satisfy MetadataSource (CapIdentify) without changing this Phase-2 code.
package googlebooks

import (
	"context"
	"fmt"
	"strings"

	gb "github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// SearchClient is the slice of *googlebooks.Client this adapter needs. It is an
// interface so tests can stub the network.
type SearchClient interface {
	Search(ctx context.Context, title, author string) ([]gb.Volume, error)
}

// Source adapts Google Books to a metasearch CoverSource.
type Source struct {
	client SearchClient
}

// New builds the adapter over a Google Books client.
func New(client SearchClient) *Source { return &Source{client: client} }

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceGoogleBooks }

// Capabilities reports cover support only (Phase 2). Phase 3 appends CapIdentify.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover}
}

// SearchCovers maps Google Books volume thumbnails to cover candidates, skipping
// volumes without an image and upgrading http image links to https (mixed-content
// safe on an HTTPS-served Folio).
func (s *Source) SearchCovers(ctx context.Context, q metasearch.Query) ([]metasearch.CoverCandidate, error) {
	vols, err := s.client.Search(ctx, q.Title, q.Author)
	if err != nil {
		return nil, fmt.Errorf("google books search: %w", err)
	}
	out := make([]metasearch.CoverCandidate, 0, len(vols))
	for i := range vols {
		thumb := vols[i].VolumeInfo.ImageLinks.Thumbnail
		if thumb == "" {
			continue
		}
		u := httpsURL(thumb)
		out = append(out, metasearch.CoverCandidate{
			Source:   metasearch.SourceGoogleBooks,
			ThumbURL: u,
			FullURL:  u,
		})
	}

	return out, nil
}

// httpsURL upgrades an http:// image link to https://.
func httpsURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		return "https://" + rest
	}

	return u
}
