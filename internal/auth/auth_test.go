package auth

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/bcrypt"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

const (
	testUser = "reader"
	testPass = "s3cret"
)

type authSuite struct {
	suite.Suite

	db   *sql.DB
	q    *dbq.Queries
	auth *Authenticator
}

func TestAuth(t *testing.T) {
	suite.Run(t, new(authSuite))
}

func (s *authSuite) SetupTest() {
	dir := s.T().TempDir()
	database, err := db.Open(slog.New(slog.DiscardHandler), dir)
	s.Require().NoError(err)
	s.db = database
	s.q = dbq.New(database)
	s.auth = New(slog.New(slog.DiscardHandler), database)
}

func (s *authSuite) TearDownTest() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

// guarded wraps a 200-OK handler in the authenticator middleware.
func (s *authSuite) guarded() http.Handler {
	return s.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func (s *authSuite) call(user, pass string, withAuth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	if withAuth {
		r.SetBasicAuth(user, pass)
	}
	w := httptest.NewRecorder()
	s.guarded().ServeHTTP(w, r)

	return w
}

func (s *authSuite) setCreds() {
	s.Require().NoError(s.auth.SetCredentials(context.Background(), new(testUser), new(testPass)))
}

func (s *authSuite) TestProtectedWhenNoCredentials() {
	s.Equal(http.StatusUnauthorized, s.call("", "", false).Code)
}

func (s *authSuite) TestProtectedWhenConfigured() {
	s.setCreds()
	s.Equal(http.StatusUnauthorized, s.call("", "", false).Code, "no credentials → 401")
	s.Equal(http.StatusUnauthorized, s.call(testUser, "wrong", true).Code, "bad password → 401")
	s.Equal(http.StatusOK, s.call(testUser, testPass, true).Code, "valid → 200")
	s.Contains(s.call("", "", false).Header().Get("WWW-Authenticate"), defaultRealm)
}

func (s *authSuite) TestWithRealmOverride() {
	a := New(slog.New(slog.DiscardHandler), s.db, WithRealm("Custom Realm"))
	s.Require().NoError(a.SetCredentials(context.Background(), new(testUser), new(testPass)))
	guarded := a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, r)
	s.Contains(w.Header().Get("WWW-Authenticate"), "Custom Realm")
}

func (s *authSuite) TestSetCredentialsHashesAndPersists() {
	s.setCreds()
	hash, err := s.q.GetSetting(context.Background(), db.SettingOPDSPassHash)
	s.Require().NoError(err)
	s.NotEqual(testPass, hash, "password must be hashed, not stored in plaintext")
	s.NoError(bcrypt.CompareHashAndPassword([]byte(hash), []byte(testPass)))
}

func (s *authSuite) TestSetCredentialsPartialUpdateLeavesPasswordIntact() {
	s.setCreds()
	// Update only the username; password must still verify.
	s.Require().NoError(s.auth.SetCredentials(context.Background(), new("newuser"), nil))
	user, set, err := s.auth.View(context.Background())
	s.Require().NoError(err)
	s.Equal("newuser", user)
	s.True(set, "password remains set after a username-only update")
	s.Equal(http.StatusOK, s.call("newuser", testPass, true).Code)
}

func (s *authSuite) TestViewReportsUnsetByDefault() {
	user, set, err := s.auth.View(context.Background())
	s.Require().NoError(err)
	s.Empty(user)
	s.False(set)
}

func (s *authSuite) TestSetCredentialsInvalidatesCache() {
	s.setCreds()
	s.Require().Equal(http.StatusOK, s.call(testUser, testPass, true).Code) // prime caches
	s.Require().NotNil(s.auth.authOK.Load(), "success cache should be primed")

	// Rotate via the service; caches must be dropped so the new password takes effect.
	const newPass = "n3wS3cret"
	s.Require().NoError(s.auth.SetCredentials(context.Background(), nil, new(newPass)))
	s.Equal(http.StatusUnauthorized, s.call(testUser, testPass, true).Code, "old password rejected")
	s.Equal(http.StatusOK, s.call(testUser, newPass, true).Code, "new password accepted")
}

func (s *authSuite) TestCredentialsCachedUntilInvalidated() {
	s.setCreds()
	s.Require().Equal(http.StatusOK, s.call(testUser, testPass, true).Code) // prime cache

	// Rotate the hash directly in the DB, bypassing the service (no invalidate).
	newHash, err := bcrypt.GenerateFromPassword([]byte("rotated"), bcrypt.DefaultCost)
	s.Require().NoError(err)
	s.Require().NoError(s.q.UpsertSetting(context.Background(),
		dbq.UpsertSettingParams{Key: db.SettingOPDSPassHash, Value: string(newHash)}))

	// Cache still holds the old hash until invalidated.
	s.Equal(http.StatusUnauthorized, s.call(testUser, "rotated", true).Code)
	s.Equal(http.StatusOK, s.call(testUser, testPass, true).Code)
}

func (s *authSuite) TestRepeatAuthHitsSuccessCache() {
	s.setCreds()
	s.Equal(http.StatusOK, s.call(testUser, testPass, true).Code)
	s.Require().NotNil(s.auth.authOK.Load(), "success cache populated after first success")
	for range 2 {
		s.Equal(http.StatusOK, s.call(testUser, testPass, true).Code)
	}
}

func (s *authSuite) TestWarnIfUnprotectedRunsBothBranches() {
	ctx := context.Background()
	s.auth.WarnIfUnprotected(ctx) // unprotected branch
	s.setCreds()
	// Fresh authenticator so WarnIfUnprotected re-reads (View is not cached).
	s.auth.WarnIfUnprotected(ctx) // protected branch
}
