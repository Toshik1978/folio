package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
)

// allowAllHosts is a blockedHost stub that permits every host, including
// loopback. Tests that need to exercise the fetch path (not the host-check path)
// install this override in SetupTest so httptest.NewServer URLs are not blocked.
func allowAllHosts(_ context.Context, _ string) bool { return false }

type editSuite struct {
	baseSuite
}

// SetupTest bypasses the SSRF host guard for the whole editSuite so that
// httptest servers bound to 127.0.0.1 can be reached by fetch-path tests.
// Dedicated SSRF tests (TestIsBlockedHost, TestSetCoverRejectsLoopbackURL,
// TestSetCoverRejectsRedirectToLoopback) restore or use the real guard directly.
func (s *editSuite) SetupTest() {
	s.baseSuite.SetupTest()
	s.books.blockedHost = allowAllHosts
}

func (s *editSuite) TearDownTest() {
	s.books.blockedHost = isBlockedHost // restore production guard after each test
	s.baseSuite.TearDownTest()
}

// rawPut issues a PUT with a raw (non-JSON) body, used for cover byte uploads.
func (s *editSuite) rawPut(path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

func (s *editSuite) TestUploadCoverPinsPriority() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.rawPut("/books/"+itoa(id)+"/cover", s.jpegFixture())
	s.Require().Equal(http.StatusOK, w.Code)

	// A cover file now exists for the book.
	s.True(s.covers.Has(id), "uploaded cover is cached on disk")

	// cover_prio is pinned to the manual sentinel so no sync downgrades it.
	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(manualCoverPrio, book.CoverPrio)
}

func (s *editSuite) TestUploadCoverRejectsNonImage() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.rawPut("/books/"+itoa(id)+"/cover", []byte("not an image"))
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUploadCoverUnknownBook() {
	w := s.rawPut("/books/999999/cover", s.jpegFixture())
	s.Equal(http.StatusNotFound, w.Code)
}

func (s *editSuite) TestSetCoverFromURL() {
	// A local HTTP server stands in for the remote image host.
	img := s.jpegFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(img)
	}))
	defer srv.Close()

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": srv.URL})
	s.Require().Equal(http.StatusOK, w.Code)
	s.True(s.covers.Has(id), "fetched cover is cached")

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(manualCoverPrio, book.CoverPrio)
}

func (s *editSuite) TestSetCoverFromURLRejectsBadScheme() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": "file:///etc/passwd"})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestSetCoverFromURLMissingURL() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUpdateBookOverwritesAndLocks() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Old", Authors: []string{"Nobody"}, Genres: []string{"History"}})

	body := map[string]any{
		"title":      "Dune",
		"authors":    []string{"Frank Herbert"},
		"genres":     []string{"Science Fiction"},
		"series":     "Dune Chronicles",
		"year":       1965,
		"publisher":  "Ace",
		"annotation": "Desert planet.",
	}
	w := s.do(http.MethodPut, "/books/"+itoa(id), body)
	s.Require().Equal(http.StatusOK, w.Code)

	var got bookView
	s.decode(w, &got)
	s.Equal("Dune", got.Title)
	s.Require().Len(got.Authors, 1)
	s.Equal("Frank Herbert", got.Authors[0].Name)
	s.Equal([]string{"Science Fiction"}, got.Tags)
	s.Require().NotNil(got.Year)
	s.Equal(1965, *got.Year)

	book, err := s.q.GetBook(s.T().Context(), id)
	s.Require().NoError(err)
	s.EqualValues(1, book.ManuallyMatched, "a manual edit locks the book against sync revert")
}

func (s *editSuite) TestUpdateBookRequiresTitle() {
	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "Old"})

	w := s.do(http.MethodPut, "/books/"+itoa(id), map[string]any{"title": "  "})
	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *editSuite) TestUpdateBookUnknown() {
	w := s.do(http.MethodPut, "/books/999999", map[string]any{"title": "x"})
	s.Equal(http.StatusNotFound, w.Code)
}

// TestIsBlockedHost unit-tests the real SSRF guard directly (no server needed).
func (s *editSuite) TestIsBlockedHost() {
	blocked := []string{
		"127.0.0.1",
		"127.0.0.1:80",
		"localhost",
		"::1",
		"[::1]:443",
		"10.0.0.1",
		"10.255.255.255",
		"172.16.0.1",
		"192.168.1.100",
		"169.254.169.254", // AWS/GCP metadata
		"169.254.169.254:80",
		"0.0.0.0",
	}
	ctx := s.T().Context()
	for _, h := range blocked {
		s.True(isBlockedHost(ctx, h), "expected %q to be blocked", h)
	}

	// A known public IP must not be blocked.
	// (8.8.8.8 is Google's DNS — globally routable, not private/loopback.)
	s.False(isBlockedHost(ctx, "8.8.8.8"), "expected public IP to be allowed")
}

// TestSetCoverRejectsLoopbackURL verifies that a literal loopback address in the
// initial URL is rejected with 400 before any fetch attempt. The real blockedHost
// is used (SetupTest installed allowAllHosts, so we restore it for this test).
func (s *editSuite) TestSetCoverRejectsLoopbackURL() {
	s.books.blockedHost = isBlockedHost // use real guard for this test
	defer func() { s.books.blockedHost = allowAllHosts }()

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	// Port 1 is almost certainly not listening; we expect rejection before any
	// network dial because parseCoverURL checks the host first.
	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": "http://127.0.0.1:1/x"})
	s.Equal(http.StatusBadRequest, w.Code)
	s.False(s.covers.Has(id), "no cover must be saved for a blocked URL")
}

// TestSetCoverRejectsRedirectToLoopback verifies that a redirect from a
// permitted host to a loopback address is blocked. The server is reached (so
// allowAllHosts is in effect for the initial URL check), but CheckRedirect uses
// the real isBlockedHost and must refuse the redirect, causing a 502.
func (s *editSuite) TestSetCoverRejectsRedirectToLoopback() {
	// The real CheckRedirect in coverFetchClient always calls isBlockedHost
	// directly — not via the blockedHost var — so this test just exercises the
	// client's redirect policy without any extra setup.
	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/evil", http.StatusFound)
	}))
	defer redirectSrv.Close()

	src := s.seedLibrary("folder", "/lib")
	id := s.seedBook(src, bookSeed{Title: "x"})

	w := s.do(http.MethodPost, "/books/"+itoa(id)+"/cover", map[string]string{"url": redirectSrv.URL})
	s.NotEqual(http.StatusOK, w.Code, "redirect to loopback must not succeed")
	s.False(s.covers.Has(id), "no cover must be saved when redirect is blocked")
}
