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
	r.Post(s.path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
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
	newWithFS(
		slog.New(slog.DiscardHandler),
		defaultHandlers(),
		"production",
		true,
		"",
		http.FS(s.testFS),
	).ServeHTTP(w, req)

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
	newWithFS(
		slog.New(slog.DiscardHandler),
		defaultHandlers(),
		"production",
		true,
		"",
		http.FS(s.testFS),
	).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "registrar loop must wire /api/ping from stubRegistrar")
}

func (s *serverTestSuite) TestOPDSRoot() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/opds/", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(
		slog.New(slog.DiscardHandler),
		defaultHandlers(),
		"production",
		true,
		"",
		http.FS(s.testFS),
	).ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	s.Equal(http.StatusOK, resp.StatusCode, "expected status OK, got %v", resp.Status)
}

func (s *serverTestSuite) TestSPARoutingRoot() {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	newWithFS(
		slog.New(slog.DiscardHandler),
		defaultHandlers(),
		"production",
		true,
		"",
		http.FS(s.testFS),
	).ServeHTTP(w, req)

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
	newWithFS(
		slog.New(slog.DiscardHandler),
		defaultHandlers(),
		"production",
		true,
		"",
		http.FS(s.testFS),
	).ServeHTTP(w, req)

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
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "", http.FS(s.testFS))

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
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "", http.FS(s.testFS))

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
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "", http.FS(fs))

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
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "", http.FS(fs))

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
	router := New(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "")
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

func (s *serverTestSuite) TestAPISameSiteGuardWiring() {
	router := newWithFS(slog.New(slog.DiscardHandler), defaultHandlers(), "production", true, "", http.FS(s.testFS))

	// A cross-site state-changing call to /api is rejected before reaching the handler.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/ping", http.NoBody)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	s.Equal(http.StatusForbidden, w.Result().StatusCode, "cross-site POST to /api must be blocked")

	// A same-origin call to the same route still works.
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/ping", http.NoBody)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	s.Equal(http.StatusOK, w.Result().StatusCode, "same-origin POST to /api must pass")
}

func (s *serverTestSuite) TestFormBodyGuard() {
	testCases := []struct {
		name        string
		method      string
		contentType string // "" means header absent
		wantStatus  int
	}{
		// Safe methods are never guarded.
		{name: "GET text/plain passes", method: http.MethodGet, contentType: "text/plain", wantStatus: http.StatusOK},

		// JSON and binary bodies are accepted on writes.
		{
			name:        "POST application/json passes",
			method:      http.MethodPost,
			contentType: "application/json",
			wantStatus:  http.StatusOK,
		},
		{
			name: "POST application/json with charset passes", method: http.MethodPost,
			contentType: "application/json; charset=utf-8", wantStatus: http.StatusOK,
		},
		{name: "PUT raw image passes", method: http.MethodPut, contentType: "image/jpeg", wantStatus: http.StatusOK},
		{name: "POST bodyless (no content-type) passes", method: http.MethodPost, wantStatus: http.StatusOK},

		// The three CORS "simple request" content types a cross-site form can forge.
		{
			name: "POST text/plain blocked", method: http.MethodPost, contentType: "text/plain",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "POST form-urlencoded blocked", method: http.MethodPost,
			contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "POST multipart blocked", method: http.MethodPost,
			contentType: "multipart/form-data; boundary=xyz", wantStatus: http.StatusUnsupportedMediaType,
		},
	}

	guard := formBodyGuard

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			var reached bool
			h := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			r := httptest.NewRequestWithContext(context.Background(), tc.method, "/api/x", http.NoBody)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			s.Equal(tc.wantStatus, w.Result().StatusCode)
			s.Equal(tc.wantStatus == http.StatusOK, reached,
				"handler should be reached only when the body type is allowed")
		})
	}
}

func (s *serverTestSuite) TestSameSiteGuard() {
	const allowed = "https://folio.example.com"

	testCases := []struct {
		name        string
		method      string
		target      string
		secFetch    string // Sec-Fetch-Site; "" means header absent
		origin      string // Origin; "" means header absent
		setSecFetch bool
		wantStatus  int
	}{
		// Safe methods are never guarded, even when cross-site.
		{
			name: "GET cross-site passes", method: http.MethodGet, target: "/api/x",
			secFetch: "cross-site", setSecFetch: true, wantStatus: http.StatusOK,
		},

		// Sec-Fetch-Site is authoritative when present.
		{
			name: "POST same-origin passes", method: http.MethodPost, target: "/api/x",
			secFetch: "same-origin", setSecFetch: true, wantStatus: http.StatusOK,
		},
		{
			name: "POST same-site passes", method: http.MethodPost, target: "/api/x",
			secFetch: "same-site", setSecFetch: true, wantStatus: http.StatusOK,
		},
		{
			name: "POST none (user-initiated) passes", method: http.MethodPost, target: "/api/x",
			secFetch: "none", setSecFetch: true, wantStatus: http.StatusOK,
		},
		{
			name: "POST cross-site blocked", method: http.MethodPost, target: "/api/x",
			secFetch: "cross-site", setSecFetch: true, wantStatus: http.StatusForbidden,
		},
		{
			name: "DELETE cross-site blocked", method: http.MethodDelete, target: "/api/x",
			secFetch: "cross-site", setSecFetch: true, wantStatus: http.StatusForbidden,
		},
		{
			name: "PUT unknown Sec-Fetch value blocked", method: http.MethodPut, target: "/api/x",
			secFetch: "bogus", setSecFetch: true, wantStatus: http.StatusForbidden,
		},

		// Fallback to Origin when Sec-Fetch-Site is absent.
		{
			name: "POST no Sec-Fetch, no Origin (non-browser) passes", method: http.MethodPost,
			target: "https://folio.example.com/api/x", wantStatus: http.StatusOK,
		},
		{
			name: "POST Origin matches PublicURL passes", method: http.MethodPost, target: "/api/x",
			origin: allowed, wantStatus: http.StatusOK,
		},
		{
			name: "POST Origin matches request host passes", method: http.MethodPost,
			target: "https://folio.example.com/api/x", origin: "https://folio.example.com",
			wantStatus: http.StatusOK,
		},
		{
			name: "POST foreign Origin blocked", method: http.MethodPost,
			target: "https://folio.example.com/api/x", origin: "https://evil.example",
			wantStatus: http.StatusForbidden,
		},
	}

	guard := sameSiteGuard(allowed)

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			var reached bool
			h := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			r := httptest.NewRequestWithContext(context.Background(), tc.method, tc.target, http.NoBody)
			if tc.setSecFetch {
				r.Header.Set("Sec-Fetch-Site", tc.secFetch)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			s.Equal(tc.wantStatus, w.Result().StatusCode)
			s.Equal(tc.wantStatus == http.StatusOK, reached,
				"handler should be reached only when the request is allowed")
		})
	}
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
