package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/suite"
)

// fakeService is a Service double recording the last SetCredentials call.
type fakeService struct {
	user     string
	set      bool
	viewErr  error
	setErr   error
	gotUser  *string
	gotPass  *string
	setCalls int
}

func (f *fakeService) View(context.Context) (string, bool, error) {
	return f.user, f.set, f.viewErr
}

func (f *fakeService) SetCredentials(_ context.Context, user, pass *string) error {
	f.setCalls++
	f.gotUser, f.gotPass = user, pass
	return f.setErr
}

type settingsSuite struct {
	suite.Suite

	svc    *fakeService
	router http.Handler
}

func TestSettings(t *testing.T) {
	suite.Run(t, new(settingsSuite))
}

func (s *settingsSuite) SetupTest() {
	s.svc = &fakeService{}
	h := New(slog.New(slog.DiscardHandler), s.svc)
	r := chi.NewRouter()
	h.Register(r)
	s.router = r
}

func (s *settingsSuite) do(method, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, "/settings", http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, "/settings", strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	return w
}

func (s *settingsSuite) TestGetReturnsView() {
	s.svc.user, s.svc.set = "reader", true
	w := s.do(http.MethodGet, "")
	s.Equal(http.StatusOK, w.Code)
	var v view
	s.Require().NoError(json.Unmarshal(w.Body.Bytes(), &v))
	s.Equal("reader", v.OPDSUser)
	s.True(v.OPDSPassSet)
}

func (s *settingsSuite) TestGetViewErrorIs500() {
	s.svc.viewErr = errors.New("boom")
	s.Equal(http.StatusInternalServerError, s.do(http.MethodGet, "").Code)
}

func (s *settingsSuite) TestPutDelegatesToSetCredentials() {
	w := s.do(http.MethodPut, `{"opds_user":"reader","opds_pass":"s3cret"}`)
	s.Equal(http.StatusOK, w.Code)
	s.Equal(1, s.svc.setCalls)
	s.Require().NotNil(s.svc.gotUser)
	s.Equal("reader", *s.svc.gotUser)
	s.Require().NotNil(s.svc.gotPass)
	s.Equal("s3cret", *s.svc.gotPass)
}

func (s *settingsSuite) TestPutPartialLeavesPassNil() {
	w := s.do(http.MethodPut, `{"opds_user":"reader"}`)
	s.Equal(http.StatusOK, w.Code)
	s.Require().NotNil(s.svc.gotUser)
	s.Nil(s.svc.gotPass, "absent opds_pass must be nil (unchanged)")
}

func (s *settingsSuite) TestPutInvalidBodyIs400() {
	s.Equal(http.StatusBadRequest, s.do(http.MethodPut, `not-json`).Code)
	s.Equal(0, s.svc.setCalls)
}

func (s *settingsSuite) TestPutSetErrorIs500() {
	s.svc.setErr = errors.New("boom")
	s.Equal(http.StatusInternalServerError, s.do(http.MethodPut, `{"opds_user":"reader"}`).Code)
}
