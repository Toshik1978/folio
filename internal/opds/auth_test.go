package opds

import (
	"net/http"
	"strconv"
)

type authSuite struct {
	baseSuite
}

func (s *authSuite) TestFeedsProtectedWhenNoCredentials() {
	s.Equal(http.StatusUnauthorized, s.get("/").Code)
	s.Equal(http.StatusUnauthorized, s.get("/search").Code)
}

func (s *authSuite) TestFeedsProtectedWhenConfigured() {
	s.setCreds()
	w401 := s.get("/")
	s.Equal(http.StatusUnauthorized, w401.Code, "no credentials → 401")
	s.Contains(
		w401.Header().Get("WWW-Authenticate"),
		"OPDS Library Manager",
		"realm must propagate in WWW-Authenticate",
	)
	s.Equal(http.StatusOK, s.getAuth("/", testUser, testPass).Code, "valid → 200")
	s.Equal(http.StatusOK, s.getAuth("/search", testUser, testPass).Code, "auth applies to all feeds")
}

func (s *authSuite) TestCoverIsPublicEvenWhenProtected() {
	s.setCreds()
	src := s.seedSource("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Book"})
	w := s.get("/books/" + itoa(id) + "/cover")
	s.Equal(http.StatusOK, w.Code)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
