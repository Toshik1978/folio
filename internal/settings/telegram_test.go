package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/suite"

	"github.com/Toshik1978/folio/internal/notify"
)

const sentinelToken = "123456:TEST-token_SENTINEL"

// fakeTelegram is a TelegramService double that records the last update.
type fakeTelegram struct {
	settings  notify.Settings
	getErr    error
	updateErr error
	testErr   error
	status    notify.DeliveryStatus

	gotUpdate   notify.Update
	updateCalls int
}

func (f *fakeTelegram) TelegramSettings(context.Context) (notify.Settings, error) {
	return f.settings, f.getErr
}

func (f *fakeTelegram) UpdateTelegram(_ context.Context, u notify.Update) (notify.Settings, error) {
	f.updateCalls++
	f.gotUpdate = u
	return f.settings, f.updateErr
}

func (f *fakeTelegram) SendTest(context.Context) error { return f.testErr }

func (f *fakeTelegram) DeliveryStatus() notify.DeliveryStatus { return f.status }

type telegramSuite struct {
	suite.Suite

	svc    *fakeTelegram
	logBuf *bytes.Buffer
	router http.Handler
}

func (s *telegramSuite) SetupTest() {
	topic := int64(42)
	ok := time.Date(2026, 11, 3, 10, 15, 2, 0, time.UTC)
	s.svc = &fakeTelegram{
		settings: notify.Settings{
			Enabled: true, TokenSet: true, TokenLast4: "ENEL", ChatID: "@test_channel",
			TopicID: &topic, Silent: true,
			Status: notify.DeliveryStatus{LastSuccessAt: &ok},
		},
		status: notify.DeliveryStatus{LastSuccessAt: &ok},
	}
	s.logBuf = &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(s.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := chi.NewRouter()
	NewTelegram(log, s.svc).Register(r)
	s.router = r
}

func (s *telegramSuite) do(method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	return w
}

func (s *telegramSuite) decode(w *httptest.ResponseRecorder) map[string]any {
	var m map[string]any
	s.Require().NoError(json.Unmarshal(w.Body.Bytes(), &m))
	return m
}

func (s *telegramSuite) TestGetReturnsRedactedView() {
	w := s.do(http.MethodGet, "/settings/telegram", "")
	s.Equal(http.StatusOK, w.Code)
	m := s.decode(w)
	s.Equal(true, m["enabled"])
	s.Equal(true, m["token_set"])
	s.Equal("ENEL", m["token_last4"])
	s.Equal("@test_channel", m["chat_id"])
	s.InDelta(42, m["topic_id"], 0)
	s.Equal(true, m["silent"])
	st, ok := m["status"].(map[string]any)
	s.Require().True(ok)
	s.Equal("2026-11-03T10:15:02Z", st["last_success_at"])
	s.Nil(st["last_failure_at"])
	s.Empty(st["last_failure_error"])
}

func (s *telegramSuite) TestGetNullTopic() {
	s.svc.settings.TopicID = nil
	m := s.decode(s.do(http.MethodGet, "/settings/telegram", ""))
	s.Nil(m["topic_id"])
	s.Contains(m, "topic_id")
}

func (s *telegramSuite) TestGetStoreError() {
	s.svc.getErr = errors.New("db down")
	w := s.do(http.MethodGet, "/settings/telegram", "")
	s.Equal(http.StatusInternalServerError, w.Code)
	s.Equal("failed to read telegram settings", s.decode(w)["error"])
	s.Contains(s.logBuf.String(), "level=ERROR")
}

func (s *telegramSuite) TestPutBotTokenKeepSemantics() {
	for name, body := range map[string]string{
		"absent": `{"enabled":true,"chat_id":"@test_channel","silent":false}`,
		"null":   `{"enabled":true,"bot_token":null,"chat_id":"@test_channel","silent":false}`,
		"empty":  `{"enabled":true,"bot_token":"","chat_id":"@test_channel","silent":false}`,
	} {
		s.Run(name, func() {
			s.svc.gotUpdate = notify.Update{}
			w := s.do(http.MethodPut, "/settings/telegram", body)
			s.Equal(http.StatusOK, w.Code)
			s.Nil(s.svc.gotUpdate.BotToken)
			s.Nil(s.svc.gotUpdate.TopicID)
			s.True(s.svc.gotUpdate.Enabled)
			s.Equal("@test_channel", s.svc.gotUpdate.ChatID)
		})
	}
}

func (s *telegramSuite) TestPutWithTokenAndTopic() {
	body := fmt.Sprintf(`{"enabled":true,"bot_token":%q,"chat_id":"-1001","topic_id":7,"silent":true}`, sentinelToken)
	w := s.do(http.MethodPut, "/settings/telegram", body)
	s.Equal(http.StatusOK, w.Code)
	s.Require().NotNil(s.svc.gotUpdate.BotToken)
	s.Equal(sentinelToken, *s.svc.gotUpdate.BotToken)
	s.Require().NotNil(s.svc.gotUpdate.TopicID)
	s.Equal(int64(7), *s.svc.gotUpdate.TopicID)
	s.True(s.svc.gotUpdate.Silent)
	s.NotContains(w.Body.String(), sentinelToken)
}

func (s *telegramSuite) TestPutErrors() {
	const body = `{"enabled":true,"chat_id":"x","silent":false}`
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"fr003", &notify.ValidationError{Field: "settings", Msg: "x"}, 400, "settings: x"},
		{"token", &notify.ValidationError{Field: "bot_token", Msg: "invalid format"}, 400, "bot_token: invalid format"},
		{"busy", fmt.Errorf("guard: %w", context.DeadlineExceeded), 503, "busy, try again"},
		{"store", errors.New("boom"), 500, "failed to save telegram settings"},
	}
	for _, c := range cases {
		s.Run(c.name, func() {
			s.svc.updateErr = c.err
			w := s.do(http.MethodPut, "/settings/telegram", body)
			s.Equal(c.code, w.Code)
			s.Equal(c.msg, s.decode(w)["error"])
		})
	}
}

func (s *telegramSuite) TestPutFR003Message() {
	s.svc.updateErr = &notify.ValidationError{Field: "enabled", Msg: "needs token"}
	w := s.do(http.MethodPut, "/settings/telegram", `{"enabled":true,"chat_id":"","silent":false}`)
	s.Equal(http.StatusBadRequest, w.Code)
	s.Equal("enabled: needs token", s.decode(w)["error"])
}

func (s *telegramSuite) TestPutBadJSON() {
	w := s.do(http.MethodPut, "/settings/telegram", `{nope`)
	s.Equal(http.StatusBadRequest, w.Code)
	s.Equal("invalid JSON body", s.decode(w)["error"])
	s.Zero(s.svc.updateCalls)
}

func (s *telegramSuite) TestTestSendSuccess() {
	w := s.do(http.MethodPost, "/settings/telegram/test", "")
	s.Equal(http.StatusOK, w.Code)
	m := s.decode(w)
	s.Equal(true, m["ok"])
	s.Contains(m, "status")
}

func (s *telegramSuite) TestTestSendNotConfigured() {
	s.svc.testErr = fmt.Errorf("send: %w", notify.ErrNotConfigured)
	w := s.do(http.MethodPost, "/settings/telegram/test", "")
	s.Equal(http.StatusConflict, w.Code)
	s.Equal("save a bot token and a channel ID first", s.decode(w)["error"])
	s.Empty(s.logBuf.String())
}

func (s *telegramSuite) TestTestSendAPIErrorLogsWarnOnly() {
	s.svc.testErr = &notify.APIError{Code: 400, Description: "Bad Request: chat not found"}
	w := s.do(http.MethodPost, "/settings/telegram/test", "")
	s.Equal(http.StatusBadGateway, w.Code)
	m := s.decode(w)
	s.Equal("Bad Request: chat not found", m["error"])
	s.Contains(m, "status")
	s.NotContains(s.logBuf.String(), "level=ERROR")
	s.Equal(1, strings.Count(s.logBuf.String(), "level=WARN"))
	s.Equal(1, strings.Count(s.logBuf.String(), "\n"))
}

func (s *telegramSuite) TestTestSendBusy() {
	s.svc.testErr = context.DeadlineExceeded
	w := s.do(http.MethodPost, "/settings/telegram/test", "")
	s.Equal(http.StatusServiceUnavailable, w.Code)
	s.Equal("busy, try again", s.decode(w)["error"])
}

func (s *telegramSuite) TestStatusOnly() {
	w := s.do(http.MethodGet, "/settings/telegram/status", "")
	s.Equal(http.StatusOK, w.Code)
	m := s.decode(w)
	s.Len(m, 3)
	s.Equal("2026-11-03T10:15:02Z", m["last_success_at"])
	s.Nil(m["last_failure_at"])
}

func (s *telegramSuite) TestNoRouteLeaksToken() {
	s.svc.settings.TokenLast4 = ""
	s.svc.status.LastFailureError = "redacted"
	s.svc.testErr = &notify.APIError{Code: 400, Description: "bad"}
	for _, rc := range []struct{ method, path, body string }{
		{http.MethodGet, "/settings/telegram", ""},
		{http.MethodPut, "/settings/telegram", fmt.Sprintf(`{"enabled":true,"bot_token":%q,"chat_id":"x","silent":false}`, sentinelToken)},
		{http.MethodPost, "/settings/telegram/test", ""},
		{http.MethodGet, "/settings/telegram/status", ""},
	} {
		w := s.do(rc.method, rc.path, rc.body)
		s.NotContains(w.Body.String(), sentinelToken, rc.path)
	}
	s.NotContains(s.logBuf.String(), sentinelToken)
}
