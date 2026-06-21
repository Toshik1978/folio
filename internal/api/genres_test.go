package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/suite"
)

type genresSuite struct {
	baseSuite
}

func TestGenresSuite(t *testing.T) {
	suite.Run(t, new(genresSuite))
}

func (s *genresSuite) TestListGenres() {
	w := s.do(http.MethodGet, "/genres", nil)
	s.Require().Equal(http.StatusOK, w.Code)

	var got []string
	s.decode(w, &got)
	s.NotEmpty(got)
	s.Contains(got, "Science Fiction")
}
