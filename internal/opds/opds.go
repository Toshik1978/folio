// Package opds serves an OPDS 1.2 catalog under /opds for reading apps such as
// Moon+ Reader and KyBook. Because /opds bypasses Cloudflare Access (mobile
// readers can't do browser SSO), auth is delegated to an injected
// auth.Authenticator — except the cover endpoint, which stays public so reader
// image loaders work.
//
// Per the dependency rules opds imports only db and covers (via an interface);
// book-file streaming is shared with the API through the bookfile package.
package opds

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// opdsPrefix is the mount path; feed links are absolute and include it.
const opdsPrefix = "/opds"

// CoverServer serves a book cover image and reports the cover-file component
// of the ?v= cache buster. *covers.Store satisfies it.
type CoverServer interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request, bookID int64)
	Version(bookID int64) string
}

// Authenticator is the OPDS Basic Auth guard. *auth.Authenticator satisfies it.
type Authenticator interface {
	Middleware(next http.Handler) http.Handler
}

// Handler holds the dependencies shared by every /opds endpoint.
type Handler struct {
	log       *slog.Logger
	db        *sql.DB
	q         *dbq.Queries
	covers    CoverServer
	authn     Authenticator
	publicURL string
}

// New builds an OPDS handler over the folio database and cover server. publicURL
// is the canonical external base URL used to build absolute feed URLs; when
// empty the (trusted) request host is used instead. authn guards the protected
// routes with Basic Auth.
func New(log *slog.Logger, database *sql.DB, covers CoverServer, authn Authenticator, publicURL string) *Handler {
	return &Handler{
		log:       log,
		db:        database,
		q:         dbq.New(database),
		covers:    covers,
		authn:     authn,
		publicURL: publicURL,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Public: covers (reader image loaders don't forward Basic Auth).
	r.Get("/books/{id}/cover", h.serveCover)

	// Protected: feeds and downloads.
	r.Group(func(pr chi.Router) {
		pr.Use(h.authn.Middleware)
		pr.Get("/", h.root)
		pr.Get("/authors", h.authors)
		pr.Get("/series", h.series)
		pr.Get("/genres", h.genres)
		pr.Get("/opensearch.xml", h.openSearch)
		pr.Get("/search", h.search)
		pr.Get("/books/{id}/files/{fileID}", h.downloadBook)
	})
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
