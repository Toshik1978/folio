package ingest

import (
	"context"
	"time"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/googlebooks"
)

type enrichSuite struct {
	baseSuite
}

// seedBook inserts a book with an optional author and ISBN for enricher lookups.
func (s *enrichSuite) seedBook(libID int64, title, author, isbn string) int64 {
	q := dbq.New(s.db)
	id, err := q.InsertBook(context.Background(), dbq.InsertBookParams{
		Title: title, LibraryID: libID, LibraryKey: title,
		Language: "en", ContentHash: title, AddedAt: time.Now().Unix(),
	})
	s.Require().NoError(err)
	if author != "" {
		aid, aerr := q.InsertAuthor(
			context.Background(),
			dbq.InsertAuthorParams{Name: author, NameFold: db.Fold(author)},
		)
		s.Require().NoError(aerr)
		s.Require().
			NoError(q.InsertBookAuthor(context.Background(), dbq.InsertBookAuthorParams{BookID: id, AuthorID: aid}))
	}
	if isbn != "" {
		s.Require().NoError(q.InsertBookIdentifier(context.Background(), dbq.InsertBookIdentifierParams{
			BookID: id, Type: "isbn", Value: isbn,
		}))
	}

	return id
}

// sampleVolume is a representative Google Books volume with one ISBN_13.
func sampleVolume() googlebooks.Volume {
	var v googlebooks.Volume
	v.ID = "abc123"
	v.VolumeInfo.Title = "Dune"
	v.VolumeInfo.Description = "Desert planet."
	v.VolumeInfo.Publisher = "Ace"
	v.VolumeInfo.PublishedDate = "1965-08-01"
	v.VolumeInfo.ImageLinks.Thumbnail = "http://img/cover.jpg"
	v.VolumeInfo.IndustryIdentifiers = append(v.VolumeInfo.IndustryIdentifiers, struct {
		Type       string `json:"type"`
		Identifier string `json:"identifier"`
	}{Type: "ISBN_13", Identifier: "9780441013593"})

	return v
}

func (s *enrichSuite) TestVolumeToMetadata() {
	meta := VolumeToMetadata(sampleVolume())
	s.Equal("Desert planet.", meta.Annotation)
	s.Equal("Ace", meta.Publisher)
	s.Equal(1965, meta.Year)
	s.Contains(meta.Identifiers, ebook.Identifier{Type: "isbn", Value: "9780441013593"})
	s.Contains(meta.Identifiers, ebook.Identifier{Type: "google", Value: "abc123"})
}
