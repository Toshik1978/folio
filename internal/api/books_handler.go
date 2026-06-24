package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/microcosm-cc/bluemonday"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/ebook"
	"github.com/Toshik1978/folio/internal/metasearch"
)

// coverFetchTimeout bounds the server-side fetch of a cover URL.
const coverFetchTimeout = 15 * time.Second

// CoverServer serves a book cover image (cache hit, lazy extraction, or
// placeholder fallback) and reports the cover-file component of the ?v= cache
// buster. *covers.Store satisfies it.
type CoverServer interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request, bookID int64)
	ServeThumbnail(w http.ResponseWriter, r *http.Request, bookID int64)
	Version(bookID int64) string
}

// MetadataExtractor lazily recovers metadata that wasn't captured at index time
// (notably INPX annotations and identifiers, which live inside the book file
// rather than the index). It is optional: a nil extractor simply means no
// backfill. The concrete implementation lives in the ingest package (which may
// parse ebooks).
type MetadataExtractor interface {
	// Backfill returns the metadata recovered from the book's source file, with
	// identifiers already cleaned. ok is false when nothing parseable was found
	// (missing book, unsupported/skipped format). The caller persists the fields
	// it needs.
	Backfill(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
}

// MetadataEnricher recovers metadata from online sources for books the local
// tiers can't fill — notably PDFs. It is optional: a nil enricher disables
// online enrichment. *metasearch.Coordinator satisfies it.
type MetadataEnricher interface {
	// Enrich looks the book up online and returns mapped metadata (with a cover).
	// ok is false when nothing matched.
	Enrich(ctx context.Context, bookID int64) (ebook.Metadata, bool, error)
	// Search returns candidates for a free-text Fix Match query.
	Search(ctx context.Context, query string) ([]metasearch.Volume, error)
	// ApplyMatch maps a specific candidate the user picked (with its cover),
	// routed by its source.
	ApplyMatch(ctx context.Context, source, id string) (ebook.Metadata, error)
}

// CoverSaver caches a freshly-acquired cover (e.g. from online enrichment) and
// reports whether the book already has a real local cover of its own.
// *covers.Store satisfies it.
type CoverSaver interface {
	Save(bookID int64, data []byte) error
	// HasLocalCover reports whether the book has a real (non-placeholder) cover
	// from its own files — cached or extractable. An online cover must never
	// replace one, so even a manual Fix Match leaves a good local cover intact.
	HasLocalCover(ctx context.Context, bookID int64) bool
}

// BooksHandler serves /books and the per-book match endpoints. It owns the lazy
// write-on-read state (backfill + online enrichment claims).
type BooksHandler struct {
	base

	db          *sql.DB
	q           *dbq.Queries
	writeGuard  *db.WriteGuard // process-wide single-writer guard, shared with the sync engine
	covers      CoverServer
	extractor   MetadataExtractor // optional; nil disables lazy backfill
	enricher    MetadataEnricher  // optional; nil disables online enrichment
	coverSaver  CoverSaver        // optional; caches online-fetched covers
	coverSearch CoverSearcher     // optional; nil disables online cover search
	// annotationPolicy sanitizes stored annotation HTML before it is served, so the
	// frontend can render it via v-html without an XSS risk. UGCPolicy permits
	// common formatting tags (p, em, strong, lists, links, …) and strips scripts,
	// event handlers, and other dangerous markup. Sanitizing here, at the serve
	// boundary, covers every library and any already-stored data.
	annotationPolicy *bluemonday.Policy

	// blockedHost is the pre-flight SSRF guard: it resolves the user-supplied host
	// and rejects one that maps to an internal address before any fetch. Tests may
	// replace it with a stub that allows loopback httptest servers while still
	// exercising fetch logic. Production code always uses isBlockedHost.
	blockedHost func(ctx context.Context, host string) bool
	// dialIPBlocked is the authoritative connect-time SSRF guard. blockedHost
	// validates the hostname before the fetch, but coverFetchClient's transport
	// resolves DNS again at dial time, so a rebinding answer (public IP at check
	// time, internal IP at dial time) could otherwise slip past the pre-flight
	// check. The dialer's Control hook runs this against the concrete IP the
	// transport is about to connect to, closing that gap. Defaults to isBlockedIP;
	// tests relax it to allow loopback httptest servers.
	dialIPBlocked func(net.IP) bool
	// coverFetchClient fetches cover URLs the user picked. A dedicated client keeps
	// the timeout off the shared default transport and rejects redirects to internal
	// addresses (SSRF guard). CheckRedirect calls isBlockedHost directly (not via
	// the blockedHost var) so the real guard is always active, even in tests that
	// override blockedHost to allow loopback httptest servers for the initial URL.
	coverFetchClient *http.Client

	lazyMu       stdsync.Mutex
	lazyInflight map[int64]bool // book ids whose lazy write-on-read tiers are running

	// editTxHook, when non-nil, runs inside updateBook's single edit transaction
	// after the scalar and identifier writes. Tests use it to force a
	// mid-transaction failure and assert the whole edit rolls back atomically. It
	// is nil in production.
	editTxHook func() error
}

// NewBooks builds the books handler. extractor, enricher, coverSaver, and coverSearch may be nil.
// writeGuard is the process-wide single-writer guard shared with the sync engine.
func NewBooks(
	log *slog.Logger,
	database *sql.DB,
	writeGuard *db.WriteGuard,
	covers CoverServer,
	extractor MetadataExtractor,
	enricher MetadataEnricher,
	coverSaver CoverSaver,
	coverSearch CoverSearcher,
) *BooksHandler {
	h := &BooksHandler{
		base:             base{log: log},
		db:               database,
		q:                dbq.New(database),
		writeGuard:       writeGuard,
		covers:           covers,
		extractor:        extractor,
		enricher:         enricher,
		coverSaver:       coverSaver,
		coverSearch:      coverSearch,
		annotationPolicy: bluemonday.UGCPolicy(),
		blockedHost:      isBlockedHost,
		dialIPBlocked:    isBlockedIP,
		lazyInflight:     map[int64]bool{},
	}
	// A dedicated client keeps the timeout off the shared default transport and
	// rejects internal addresses (SSRF guard). The dialer's Control hook is the
	// authoritative guard: it validates the concrete IP the transport dials, so a
	// DNS-rebinding answer cannot slip an internal IP past the pre-flight host
	// check. CheckRedirect re-checks each hop's host (via isBlockedHost directly,
	// not the blockedHost var) so the real guard is always active even in tests
	// that override blockedHost to allow loopback httptest servers.
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   h.controlDial,
	}
	h.coverFetchClient = &http.Client{
		Timeout: coverFetchTimeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}

			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http(s) scheme %q", req.URL.Scheme)
			}

			if isBlockedHost(req.Context(), req.URL.Host) {
				return errors.New("redirect to internal address blocked")
			}

			return nil
		},
	}

	return h
}

func (h *BooksHandler) Register(r chi.Router) {
	r.Route("/books", func(r chi.Router) { //nolint:dupl // structurally similar but distinct routes
		r.Get("/", h.listBooks)
		r.Get("/{id}", h.getBook)
		r.Get("/{id}/files/{fileID}", h.downloadBook)
		r.Get("/{id}/cover", h.serveCover)
		r.Get("/{id}/cover/thumbnail", h.serveThumbnail)
		r.Get("/{id}/cover/search", h.searchCovers)
		r.Get("/{id}/match", h.searchMatch)
		r.Post("/{id}/match", h.applyMatch)
		r.Put("/{id}", h.updateBook)
		r.Put("/{id}/cover", h.uploadCover)
		r.Post("/{id}/cover", h.setCoverFromURL)
	})
}

// controlDial is the net.Dialer Control hook on coverFetchClient: it runs after
// DNS resolution with the concrete address the transport is about to connect to,
// rejecting any dial whose resolved IP is internal. Because it inspects the IP
// actually being dialed (not a separately-resolved hostname), it is immune to the
// DNS-rebinding TOCTOU that a pre-flight host check alone cannot prevent.
func (h *BooksHandler) controlDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("split dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || h.dialIPBlocked(ip) {
		return fmt.Errorf("dial to blocked address %q", address)
	}

	return nil
}
