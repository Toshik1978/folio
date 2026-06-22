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

// fakeGB stubs the Google Books client, recording the last query it received.
type fakeGB struct {
	isbnVol    googlebooks.Volume
	isbnOK     bool
	searchVols []googlebooks.Volume
	volume     googlebooks.Volume
	image      []byte
	lastISBN   string
	lastTitle  string
	lastAuthor string
}

func (f *fakeGB) SearchISBN(_ context.Context, isbn string) (googlebooks.Volume, bool, error) {
	f.lastISBN = isbn
	return f.isbnVol, f.isbnOK, nil
}

func (f *fakeGB) Search(_ context.Context, title, author string) ([]googlebooks.Volume, error) {
	f.lastTitle, f.lastAuthor = title, author
	return f.searchVols, nil
}

func (f *fakeGB) SearchQuery(_ context.Context, q string) ([]googlebooks.Volume, error) {
	f.lastTitle = q
	return f.searchVols, nil
}

func (f *fakeGB) GetVolume(context.Context, string) (googlebooks.Volume, error) {
	return f.volume, nil
}

func (f *fakeGB) FetchImage(context.Context, string) ([]byte, error) {
	return f.image, nil
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

func (s *enrichSuite) TestEnrichByISBN() {
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Dune", "Frank Herbert", "9780441013593")
	gb := &fakeGB{isbnVol: sampleVolume(), isbnOK: true, image: []byte("COVER")}

	meta, ok, err := NewEnricher(s.db, gb).Enrich(context.Background(), id)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("9780441013593", gb.lastISBN, "ISBN lookup is preferred")
	s.Equal("Desert planet.", meta.Annotation)
	s.Equal([]byte("COVER"), meta.Cover)
}

func (s *enrichSuite) TestEnrichFallsBackToTitleAuthor() {
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Dune", "Frank Herbert", "") // no ISBN on record
	gb := &fakeGB{searchVols: []googlebooks.Volume{sampleVolume()}}

	meta, ok, err := NewEnricher(s.db, gb).Enrich(context.Background(), id)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("Dune", gb.lastTitle)
	s.Equal("Frank Herbert", gb.lastAuthor)
	s.Equal("Ace", meta.Publisher)
}

func (s *enrichSuite) TestEnrichNoMatch() {
	src := s.insertLibrary("folder", "/lib")
	id := s.seedBook(src.ID, "Unknown", "", "")
	gb := &fakeGB{searchVols: nil} // empty search results

	_, ok, err := NewEnricher(s.db, gb).Enrich(context.Background(), id)
	s.Require().NoError(err)
	s.False(ok)
}
