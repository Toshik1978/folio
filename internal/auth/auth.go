// Package auth owns OPDS Basic Auth credentials: storage in the settings table,
// password hashing, verification with a burst-friendly cache, the HTTP Basic
// Auth middleware, and the one-shot startup seed/warn helpers. It is the single
// home for credential knowledge, shared by the opds handler (verification, via
// Middleware) and the settings handler (View / SetCredentials).
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"golang.org/x/crypto/bcrypt"

	"github.com/Toshik1978/folio/internal/db/dbq"
)

// defaultRealm is the Basic Auth realm advertised in WWW-Authenticate when no
// override is supplied via WithRealm.
const defaultRealm = "OPDS Library Manager"

// Authenticator owns OPDS credential storage, verification, and the auth caches.
type Authenticator struct {
	log       *slog.Logger
	db        *sql.DB
	q         *dbq.Queries
	realm     string
	dummyHash []byte
	creds     atomic.Pointer[credentials]       // cached OPDS auth; nil = not loaded
	authOK    atomic.Pointer[[sha256.Size]byte] // key of the last successful Basic Auth; nil = none
}

// Option configures an Authenticator at construction.
type Option func(*Authenticator)

// WithRealm overrides the Basic Auth realm. An empty value is ignored.
func WithRealm(realm string) Option {
	return func(a *Authenticator) {
		if realm != "" {
			a.realm = realm
		}
	}
}

// New builds an Authenticator over the folio database.
func New(log *slog.Logger, database *sql.DB, opts ...Option) *Authenticator {
	a := &Authenticator{
		log:       log,
		db:        database,
		q:         dbq.New(database),
		realm:     defaultRealm,
		dummyHash: mustDummyHash(),
	}
	for _, opt := range opts {
		opt(a)
	}

	return a
}

func mustDummyHash() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("folio-opds-dummy"), bcrypt.DefaultCost)
	if err != nil {
		panic(err) // bcrypt fails only on an invalid cost; DefaultCost is valid
	}

	return h
}

// WarnIfUnprotected logs a severe warning when the catalog has no credentials
// and is therefore can't be served unauthenticated. Call once at startup.
func (a *Authenticator) WarnIfUnprotected(ctx context.Context) {
	c, err := a.loadCredentials(ctx)
	if err != nil || c.user == "" || c.hash == "" {
		a.log.Warn("SECURITY: OPDS catalog has no credentials configured and can't be served UNAUTHENTICATED; " +
			"configure credentials via PUT /api/settings")
	}
}

// Middleware guards the catalog with HTTP Basic Auth using credentials from the
// settings table. When no credentials are configured every protected route is
// rejected with 401 (the catalog is closed, not served unprotected) and a startup
// warning is logged via WarnIfUnprotected; credentials must be set via
// PUT /api/settings before OPDS will serve.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, hash, configured := a.credentials(r.Context())
		if !configured {
			http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
			return
		}

		reqUser, reqPass, ok := r.BasicAuth()
		if ok && a.verifyCredentials(user, hash, reqUser, reqPass) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q`, a.realm))
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
	})
}

// verifyCredentials checks one Basic Auth pair against the configured user and
// bcrypt hash. Reading apps fetch feeds, covers, and files in bursts, and bcrypt
// costs ~100ms per verify by design, so the last successful pair is cached under
// a SHA-256 key (which embeds the stored hash, so a credential change
// invalidates it implicitly; invalidate clears it explicitly). A username
// mismatch still burns a full bcrypt compare against a dummy hash so response
// time doesn't reveal which usernames exist.
func (a *Authenticator) verifyCredentials(user, hash, reqUser, reqPass string) bool {
	key := authKey(reqUser, reqPass, hash)
	if cached := a.authOK.Load(); cached != nil && subtle.ConstantTimeCompare(cached[:], key[:]) == 1 {
		return true
	}

	userOK := subtle.ConstantTimeCompare([]byte(reqUser), []byte(user)) == 1
	cmp := []byte(hash)

	if !userOK {
		cmp = a.dummyHash
	}

	if bcrypt.CompareHashAndPassword(cmp, []byte(reqPass)) != nil || !userOK {
		return false
	}

	a.authOK.Store(&key)

	return true
}

// authKey derives the auth-cache key for a verified pair; including the stored
// hash ties the entry to the current credentials.
func authKey(user, pass, hash string) [sha256.Size]byte {
	return sha256.Sum256([]byte(user + "\x00" + pass + "\x00" + hash))
}

// credentials is the cached OPDS auth pair. The whole struct is swapped
// atomically, so readers never see a half-updated value.
type credentials struct {
	user, hash string
	configured bool // both user and hash are set
}

// credentials returns the configured OPDS username and password hash, reading
// them from the settings table once and caching the result. invalidate drops the
// cache after a credential change.
func (a *Authenticator) credentials(ctx context.Context) (user, hash string, configured bool) {
	if c := a.creds.Load(); c != nil {
		return c.user, c.hash, c.configured
	}
	c, err := a.loadCredentials(ctx)
	if err != nil {
		// Transient read error: behave as unconfigured but don't cache it, so a
		// momentary DB hiccup can't stick the catalog open.
		return "", "", false
	}
	a.creds.Store(c)

	return c.user, c.hash, c.configured
}

// invalidate drops both the credentials cache and the auth-success cache so the
// next protected request re-reads them. Called by SetCredentials.
func (a *Authenticator) invalidate() {
	a.creds.Store(nil)
	a.authOK.Store(nil)
}
