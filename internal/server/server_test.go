package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/suite"
)

// stubRegistrar registers a single GET handler at the given path.
type stubRegistrar struct{ path string }

func (s stubRegistrar) Register(r chi.Router) {
	r.Get(s.path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// stubOPDS is a minimal OPDS for tests.
type stubOPDS struct{}

func (stubOPDS) Register(r chi.Router) {
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func defaultHandlers() Handlers {
	return Handlers{
		API:  []Registrar{stubRegistrar{path: "/ping"}},
		OPDS: stubOPDS{},
	}
}

func TestServer(t *testing.T) {
	suite.Run(t, new(serverTestSuite))
}

type serverTestSuite struct {
	suite.Suite

	testIndexFile string
	testFS        fstest.MapFS
}

func (s *serverTestSuite) SetupSuite() {
	s.testIndexFile = "index.html"
	s.testFS = fstest.MapFS{
		s.testIndexFile: &fstest.MapFile{
			Data: []byte("<html><body>FolioIdx Placeholder</body></html>"),
		},
	}
}

func (s *serverTestSuite) TestAPIHealth() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/health", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS)).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)
	s.Contains(resp.Header.Get("Content-Type"), "application/json", "expected JSON content-type")

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err, "failed to read body: %v", err)
	s.Contains(string(body), `"status":"ok"`, "expected health body to contain status ok")
}

func (s *serverTestSuite) TestAPIRegistrarLoop() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/ping", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS)).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "registrar loop must wire /api/ping from stubRegistrar")
}

func (s *serverTestSuite) TestOPDSRoot() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/opds/", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS)).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)
}

func (s *serverTestSuite) TestSPARoutingRoot() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS)).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err, "failed to read body: %v", err)
	s.Contains(
		string(body),
		"FolioIdx Placeholder",
		"expected body to contain placeholder Vue app, got %q",
		string(body),
	)
}

func (s *serverTestSuite) TestSPARoutingFallback() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dashboard", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS)).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err, "failed to read body: %v", err)
	s.Contains(
		string(body),
		"FolioIdx Placeholder",
		"expected body to contain fallback index.html content, got %q",
		string(body),
	)
}

func (s *serverTestSuite) TestAPIAndOPDSFallbackProtection() {
	testCases := []struct {
		path string
	}{
		// Unmatched API/OPDS routes must 404 (never fall through to the SPA).
		// The mounted roots (/api, /opds/) are real endpoints, so they are not
		// listed here.
		{"/api"},
		{"/api/"},
		{"/api/invalid-route"},
		{"/api/v1/invalid-route"},
		{"/opds/invalid-route"},
		{"/opds/nope/deep"},
	}
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS))

	for _, tc := range testCases {
		s.Run(tc.path, func() {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, http.NoBody)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			s.Equal(http.StatusNotFound, resp.StatusCode,
				"expected status %d (Not Found) for path %q, got %d", http.StatusNotFound, tc.path, resp.StatusCode)
		})
	}
}

func (s *serverTestSuite) TestStaticAssetsFallbackProtection() {
	testCases := []struct {
		path string
	}{
		{"/assets/missing.js"},
		{"/favicon.ico"},
		{"/css/app.css"},
		{"/images/logo.png"},
	}
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(s.testFS))

	for _, tc := range testCases {
		s.Run(tc.path, func() {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, http.NoBody)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			s.Equal(http.StatusNotFound, resp.StatusCode,
				"expected status %d (Not Found) for path %q, got %d", http.StatusNotFound, tc.path, resp.StatusCode)
		})
	}
}

func (s *serverTestSuite) TestStaticAssetsServing() {
	fs := fstest.MapFS{
		s.testIndexFile: &fstest.MapFile{
			Data: []byte("<html><body>FolioIdx Placeholder</body></html>"),
		},
		"assets/main.js": &fstest.MapFile{
			Data: []byte("console.log('hello');"),
		},
		"favicon.ico": &fstest.MapFile{
			Data: []byte("icon-data"),
		},
	}
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(fs))

	testCases := []struct {
		path         string
		expectedBody string
	}{
		{"/assets/main.js", "console.log('hello');"},
		{"/favicon.ico", "icon-data"},
	}

	for _, tc := range testCases {
		s.Run(tc.path, func() {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, http.NoBody)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			s.Equal(http.StatusOK, resp.StatusCode,
				"expected status %d (OK) for path %q, got %d", http.StatusOK, tc.path, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			s.Require().NoError(err, "failed to read body: %v", err)
			s.Equal(tc.expectedBody, string(body), "expected body %q, got %q", tc.expectedBody, string(body))
		})
	}
}

func (s *serverTestSuite) TestSPARoutingDirectoryFallback() {
	fs := fstest.MapFS{
		s.testIndexFile: &fstest.MapFile{
			Data: []byte("<html><body>FolioIdx Placeholder</body></html>"),
		},
		"assets/style.css": &fstest.MapFile{
			Data: []byte("body {}"),
		},
	}
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, http.FS(fs))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/", http.NoBody)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err, "failed to read body: %v", err)
	s.Contains(
		string(body),
		"FolioIdx Placeholder",
		"expected body to contain fallback index.html, got %q",
		string(body),
	)
	s.NotContains(string(body), "<a href=", "unexpected directory listing content, got %q", string(body))
}

func (s *serverTestSuite) TestNewServer() {
	router := New(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true)
	s.NotNil(router)
}

func (s *serverTestSuite) TestSloggerPrint() {
	sl := &slogger{slog.New(slog.DiscardHandler)}
	sl.Print("print log test")
}

func (s *serverTestSuite) TestServeStaticFileUsesOpenHandle() {
	fs := http.FS(fstest.MapFS{"app.js": &fstest.MapFile{Data: []byte("console.log(1)")}})

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/app.js", http.NoBody)
	s.True(serveStaticFile(fs, w, r))
	s.Equal("console.log(1)", w.Body.String())
	s.Contains(w.Header().Get("Content-Type"), "javascript")

	w = httptest.NewRecorder()
	r = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/missing.js", http.NoBody)
	s.False(serveStaticFile(fs, w, r), "missing file falls through to the SPA fallback")
}

func (s *serverTestSuite) TestProxyHeadersClampsForwardedProto() {
	var got string
	h := proxyHeaders(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.URL.Scheme
	}))

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	r.Header.Set("X-Forwarded-Proto", "javascript")
	h.ServeHTTP(httptest.NewRecorder(), r)
	s.Equal("http", got, "non-http(s) forwarded proto must be ignored")

	r = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	r.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(httptest.NewRecorder(), r)
	s.Equal("https", got)
}
