package ebook

import (
	"bytes"
	"context"
)

type pdfSuite struct {
	baseSuite
}

func (s *pdfSuite) TestTraits() {
	s.Equal(Traits{Rank: 1, Describes: false}, pdfParser{}.Traits())
}

func (s *pdfSuite) TestMetadataSource() {
	src := s.open(s.fixture("test.pdf"))

	m, err := pdfParser{}.Metadata(context.Background(), src)
	s.Require().NoError(err)

	s.Equal("Test PDF Book", m.Title)
	s.Equal([]string{"Pat Draftson"}, m.Authors)
	s.Equal("PDF test annotation.", m.Annotation)
	s.Positive(m.Pages)
	s.Empty(m.Cover)
}

func (s *pdfSuite) TestCoverSource() {
	src := s.open(s.fixture("test.pdf"))

	cover, err := pdfParser{}.Cover(context.Background(), src)
	s.Require().NoError(err)

	s.True(bytes.HasPrefix(cover, []byte("\x89PNG\r\n\x1a\n")), "cover should be a PNG image")
}

func (s *pdfSuite) TestSourceCancelled() {
	src := s.open(s.fixture("test.pdf"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pdfParser{}.Metadata(ctx, src)
	s.Require().ErrorIs(err, context.Canceled)
	_, err = pdfParser{}.Cover(ctx, src)
	s.Require().ErrorIs(err, context.Canceled)
}

func (s *pdfSuite) TestSourceMalformed() {
	data := []byte("not a pdf")
	src := Source{Name: "bad.pdf", R: bytes.NewReader(data), Size: int64(len(data))}

	_, err := pdfParser{}.Metadata(context.Background(), src)
	s.Require().ErrorIs(err, ErrMalformed)
	_, err = pdfParser{}.Cover(context.Background(), src)
	s.Require().ErrorIs(err, ErrMalformed)
}
