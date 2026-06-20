package server

import (
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	apipkg "github.com/Toshik1978/folio/internal/api"
	"github.com/Toshik1978/folio/web"
)

// Registrar mounts its routes on a chi.Router. The api and settings handlers
// satisfy it.
type Registrar interface {
	Register(r chi.Router)
}

// Handlers are the built HTTP handlers, supplied by the composition root.
type Handlers struct {
	API  []Registrar // mounted under /api in order
	OPDS Registrar   // mounted at /opds
}

func New(log *slog.Logger, h Handlers, env string, noColor bool) *chi.Mux {
	return newWithFS(log, h, env, noColor, web.GetFileSystem())
}

func newWithFS(log *slog.Logger, h Handlers, env string, noColor bool, fs http.FileSystem) *chi.Mux {
	r := chi.NewRouter()

	r.Use(proxyHeaders)
	if env == "development" {
		middleware.DefaultLogger = middleware.RequestLogger(&middleware.DefaultLogFormatter{
			Logger:  &slogger{log},
			NoColor: noColor,
		})
		r.Use(middleware.Logger)
	}
	r.Use(middleware.Recoverer)

	r.Route("/api", func(api chi.Router) {
		api.Get("/health", apipkg.Health)
		for _, reg := range h.API {
			reg.Register(api)
		}
	})
	r.Route("/opds", func(o chi.Router) {
		h.OPDS.Register(o)
	})

	// Web handlers
	r.HandleFunc("/*", webHandler(fs))

	return r
}

func webHandler(fs http.FileSystem) http.HandlerFunc {
	fileServer := http.FileServer(fs)

	return func(w http.ResponseWriter, req *http.Request) {
		reqPath := req.URL.Path

		if reqPath == "/" || reqPath == "" {
			fileServer.ServeHTTP(w, req)
			return
		}

		if serveStaticFile(fs, w, req) {
			return
		}

		ext := path.Ext(reqPath)
		if ext != "" && ext != ".html" {
			http.NotFound(w, req)
			return
		}

		req.URL.Path = "/"
		fileServer.ServeHTTP(w, req)
	}
}

// serveStaticFile serves req's path when it resolves to a real file in fs,
// writing from the already-open handle instead of re-opening it through
// FileServer. It returns false (not found / directory) so the caller can fall
// through to the SPA fallback.
func serveStaticFile(fs http.FileSystem, w http.ResponseWriter, req *http.Request) bool {
	f, err := fs.Open(strings.TrimPrefix(req.URL.Path, "/"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	// ServeContent derives Content-Type from the name's extension and handles
	// Range/If-* like FileServer. The embedded FS reports zero mod times, so
	// Last-Modified is simply omitted — same as before.
	http.ServeContent(w, req, stat.Name(), stat.ModTime(), f)

	return true
}
