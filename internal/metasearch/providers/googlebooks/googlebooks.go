// Package googlebooks adapts the low-level googlebooks.Client to the metasearch
// provider model as a dual-capability Source: it supplies cover candidates
// (CapCover) and structured metadata (CapIdentify). The volume→metadata mapping
// is injected so the shared ingest normalization (genres, identifiers, year) is
// reused rather than duplicated.
package googlebooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/Toshik1978/folio/internal/ebook"
	gb "github.com/Toshik1978/folio/internal/googlebooks"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// Client is the slice of *googlebooks.Client this adapter needs. It is an
// interface so tests can stub the network.
type Client interface {
	Search(ctx context.Context, title, author string) ([]gb.Volume, error)
	SearchQuery(ctx context.Context, q string) ([]gb.Volume, error)
	SearchISBN(ctx context.Context, isbn string) (gb.Volume, bool, error)
	GetVolume(ctx context.Context, id string) (gb.Volume, error)
	FetchImage(ctx context.Context, url string) ([]byte, error)
}

// Source adapts Google Books to a metasearch cover + metadata source.
type Source struct {
	client    Client
	mapVolume func(gb.Volume) ebook.Metadata
}

// New builds the adapter over a Google Books client and a volume→metadata
// mapper (in production, ingest.VolumeToMetadata).
func New(client Client, mapVolume func(gb.Volume) ebook.Metadata) *Source {
	return &Source{client: client, mapVolume: mapVolume}
}

// Name identifies the source.
func (s *Source) Name() string { return metasearch.SourceGoogleBooks }

// Capabilities reports cover and metadata support.
func (s *Source) Capabilities() []metasearch.Capability {
	return []metasearch.Capability{metasearch.CapCover, metasearch.CapIdentify}
}

// SearchCovers maps Google Books volume thumbnails to cover candidates.
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
			Source: metasearch.SourceGoogleBooks, ThumbURL: u, FullURL: upscale(u),
		})
	}

	return out, nil
}

// Search returns lightweight metadata candidates. It preserves the legacy query
// strategy: ISBN lookup when an ISBN is present (highest accuracy), the
// structured intitle/inauthor search when an author is known, otherwise the raw
// free-text query the Fix-Match UI passes.
func (s *Source) Search(ctx context.Context, q metasearch.Query) ([]metasearch.Volume, error) {
	switch {
	case q.ISBN != "":
		vol, ok, err := s.client.SearchISBN(ctx, q.ISBN)
		if err != nil {
			return nil, fmt.Errorf("google books isbn: %w", err)
		}
		if !ok {
			return nil, nil
		}

		return []metasearch.Volume{s.toVolume(vol)}, nil
	case q.Author != "":
		return s.mapVolumes(s.client.Search(ctx, q.Title, q.Author))
	default:
		return s.mapVolumes(s.client.SearchQuery(ctx, q.Title))
	}
}

// Get fetches the full volume by id, maps it via the injected mapper, and
// gap-fills the cover by downloading the thumbnail (best effort — a failed image
// fetch just leaves Cover empty, matching the legacy enricher).
func (s *Source) Get(ctx context.Context, id string) (ebook.Metadata, error) {
	vol, err := s.client.GetVolume(ctx, id)
	if err != nil {
		return ebook.Metadata{}, fmt.Errorf("get volume: %w", err)
	}
	meta := s.mapVolume(vol)
	if thumb := vol.VolumeInfo.ImageLinks.Thumbnail; thumb != "" {
		if data, ferr := s.client.FetchImage(ctx, thumb); ferr == nil {
			meta.Cover = data
		}
	}

	return meta, nil
}

// mapVolumes maps a (volumes, err) result to lightweight candidates.
func (s *Source) mapVolumes(vols []gb.Volume, err error) ([]metasearch.Volume, error) {
	if err != nil {
		return nil, fmt.Errorf("google books search: %w", err)
	}
	out := make([]metasearch.Volume, 0, len(vols))
	for i := range vols {
		out = append(out, s.toVolume(vols[i]))
	}

	return out, nil
}

// toVolume maps a Google Books volume to the neutral candidate type.
func (s *Source) toVolume(v gb.Volume) metasearch.Volume {
	return metasearch.Volume{
		Source:       metasearch.SourceGoogleBooks,
		ID:           v.ID,
		Title:        v.VolumeInfo.Title,
		Authors:      v.VolumeInfo.Authors,
		Year:         ebook.ParseYear(v.VolumeInfo.PublishedDate),
		ThumbnailURL: httpsURL(v.VolumeInfo.ImageLinks.Thumbnail),
	}
}

// upscale returns a slightly higher-quality Google Books image URL by removing
// the &edge=curl decoration (which adds a page-curl rendering artefact and
// implies a smaller image). The transform is conservative — no zoom params are
// added — so a rare miss simply falls back to the thumbnail, which the server
// validates on apply anyway.
func upscale(u string) string {
	// Strip all three positional variants of the edge=curl parameter.
	for _, pat := range []string{"&edge=curl", "edge=curl&", "edge=curl"} {
		if stripped := strings.ReplaceAll(u, pat, ""); stripped != u {
			return stripped
		}
	}

	return u
}

// httpsURL upgrades an http:// image link to https://.
func httpsURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		return "https://" + rest
	}

	return u
}
