package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

// GoogleClient is the subset of *googlebooks.Client the enricher uses. It is an
// interface so tests can stub the network.
type GoogleClient interface {
	SearchISBN(ctx context.Context, isbn string) (googlebooks.Volume, bool, error)
	Search(ctx context.Context, title, author string) ([]googlebooks.Volume, error)
	SearchQuery(ctx context.Context, q string) ([]googlebooks.Volume, error)
	GetVolume(ctx context.Context, id string) (googlebooks.Volume, error)
	FetchImage(ctx context.Context, url string) ([]byte, error)
}

// Enricher recovers metadata for sparse books (PDFs, folder imports) from the
// Google Books API — the online tier of the metadata pipeline. It reads the
// folio database to build the lookup query and maps results into ebook.Metadata,
// reusing the same identifier/genre/year normalization as sync.
type Enricher struct {
	db     *sql.DB
	client GoogleClient
}

// NewEnricher builds an Enricher over the folio database and a Google Books client.
func NewEnricher(db *sql.DB, client GoogleClient) *Enricher {
	return &Enricher{db: db, client: client}
}

// Enrich looks the book up on Google Books by its ISBN (preferred) or
// title+author and returns the mapped metadata, including a downloaded cover.
// ok is false when nothing matched.
func (e *Enricher) Enrich(ctx context.Context, bookID int64) (ebook.Metadata, bool, error) {
	q := dbq.New(e.db)
	book, err := q.GetBook(ctx, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		return ebook.Metadata{}, false, nil
	}
	if err != nil {
		return ebook.Metadata{}, false, fmt.Errorf("get book: %w", err)
	}

	vol, ok, err := e.lookup(ctx, q, book)
	if err != nil || !ok {
		return ebook.Metadata{}, false, err
	}

	return e.toMetadataWithCover(ctx, vol), true, nil
}

// Search returns Google Books candidates for a free-text Fix Match query.
func (e *Enricher) Search(ctx context.Context, query string) ([]googlebooks.Volume, error) {
	vols, err := e.client.SearchQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search google books: %w", err)
	}

	return vols, nil
}

// ApplyMatch fetches a specific volume chosen by the user (Fix Match) and maps
// it, including its cover.
func (e *Enricher) ApplyMatch(ctx context.Context, volumeID string) (ebook.Metadata, error) {
	vol, err := e.client.GetVolume(ctx, volumeID)
	if err != nil {
		return ebook.Metadata{}, fmt.Errorf("get volume: %w", err)
	}

	return e.toMetadataWithCover(ctx, vol), nil
}

// lookup finds the best-matching volume for book: by ISBN when one is on record,
// otherwise a fuzzy title + first-author search.
func (e *Enricher) lookup(ctx context.Context, q *dbq.Queries, book dbq.Book) (googlebooks.Volume, bool, error) {
	ids, err := q.ListIdentifiersForBook(ctx, book.ID)
	if err != nil {
		return googlebooks.Volume{}, false, fmt.Errorf("list identifiers: %w", err)
	}
	for _, id := range ids {
		if id.Type == isbnType {
			vol, ok, serr := e.client.SearchISBN(ctx, id.Value)
			if serr != nil {
				return googlebooks.Volume{}, false, fmt.Errorf("search isbn: %w", serr)
			}

			return vol, ok, nil
		}
	}

	author, err := e.firstAuthor(ctx, q, book.ID)
	if err != nil {
		return googlebooks.Volume{}, false, err
	}
	vols, err := e.client.Search(ctx, book.Title, author)
	if err != nil {
		return googlebooks.Volume{}, false, fmt.Errorf("search google books: %w", err)
	}
	if len(vols) == 0 {
		return googlebooks.Volume{}, false, nil
	}

	return vols[0], true, nil
}

// firstAuthor returns the book's first author name, or "" when it has none.
func (e *Enricher) firstAuthor(ctx context.Context, q *dbq.Queries, bookID int64) (string, error) {
	authors, err := q.ListAuthorsForBook(ctx, bookID)
	if err != nil {
		return "", fmt.Errorf("list authors: %w", err)
	}
	if len(authors) == 0 {
		return "", nil
	}

	return authors[0].Name, nil
}

// toMetadataWithCover maps a volume and downloads its cover thumbnail (best
// effort — a failed image fetch just leaves Cover empty).
func (e *Enricher) toMetadataWithCover(ctx context.Context, vol googlebooks.Volume) ebook.Metadata {
	meta := VolumeToMetadata(vol)
	if vol.VolumeInfo.ImageLinks.Thumbnail != "" {
		if data, err := e.client.FetchImage(ctx, vol.VolumeInfo.ImageLinks.Thumbnail); err == nil {
			meta.Cover = data
		}
	}

	return meta
}

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
