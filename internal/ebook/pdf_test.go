package ebook

import (
	"bytes"
	"context"
)

type pdfSuite struct {
	baseSuite
}

func (s *pdfSuite) TestParse() {
	m, err := s.d.Parse(context.Background(), s.log, s.fixture("test.pdf"))
	s.Require().NoError(err)

	s.Equal("Test PDF Book", m.Title)
	s.Equal([]string{"Pat Draftson"}, m.Authors)
	s.Equal("PDF test annotation.", m.Annotation)

	s.NotEmpty(m.Cover)
	s.True(bytes.HasPrefix(m.Cover, []byte("\x89PNG\r\n\x1a\n")), "cover should be a PNG image")
}
