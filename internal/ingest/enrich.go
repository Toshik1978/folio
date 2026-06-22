package ingest

import (
	"strings"

	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

// VolumeToMetadata maps a Google Books volume to Folio's domain metadata,
// reusing ingest's identifier cleaning, year parsing, and genre normalization so
// online-sourced data lands identical to locally-parsed data. Exported so the
// metasearch Google Books adapter can reuse the exact same mapping.
func VolumeToMetadata(v googlebooks.Volume) ebook.Metadata {
	raw := make([]ebook.Identifier, 0, len(v.VolumeInfo.IndustryIdentifiers)+1)
	for _, ii := range v.VolumeInfo.IndustryIdentifiers {
		raw = append(raw, ebook.Identifier{Type: ii.Type, Value: ii.Identifier})
	}
	if v.ID != "" {
		raw = append(raw, ebook.Identifier{Type: "google", Value: v.ID})
	}

	return ebook.Metadata{
		Title:       strings.TrimSpace(v.VolumeInfo.Title),
		Authors:     v.VolumeInfo.Authors,
		Annotation:  strings.TrimSpace(v.VolumeInfo.Description),
		Publisher:   strings.TrimSpace(v.VolumeInfo.Publisher),
		Year:        ebook.ParseYear(v.VolumeInfo.PublishedDate),
		Genres:      normalizeGenres(v.VolumeInfo.Categories),
		Identifiers: cleanedEbookIdentifiers(raw),
	}
}
