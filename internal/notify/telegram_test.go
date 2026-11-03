package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/stretchr/testify/suite"
)

type clientSuite struct {
	suite.Suite
}

type captured struct {
	method, path, ctype string
	body                map[string]any
}

func (s *clientSuite) server(status int, body string, got *captured) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			raw, _ := io.ReadAll(r.Body)
			got.method, got.path, got.ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
			_ = json.Unmarshal(raw, &got.body)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func (s *clientSuite) cfg() Config {
	return Config{Enabled: true, BotToken: testToken, ChatID: testChat}
}

func (s *clientSuite) TestRequestShape() {
	var got captured
	srv := s.server(200, `{"ok":true}`, &got)
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	s.Require().NoError(c.Send(context.Background(), s.cfg(), "hello"))
	s.Equal(http.MethodPost, got.method)
	s.Equal("/bot"+testToken+"/sendMessage", got.path)
	s.Equal("application/json", got.ctype)
	s.Equal(map[string]any{
		"chat_id":              testChat,
		"text":                 "hello",
		"disable_notification": false,
		"link_preview_options": map[string]any{"is_disabled": true},
	}, got.body)

	cfg := s.cfg()
	cfg.TopicID, cfg.Silent = 42, true
	s.Require().NoError(c.Send(context.Background(), cfg, "hi"))
	s.EqualValues(42, got.body["message_thread_id"])
	s.Equal(true, got.body["disable_notification"])
	s.NotContains(got.body, "parse_mode")
}

func (s *clientSuite) TestAPIErrors() {
	srv := s.server(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, nil)
	defer srv.Close()
	err := NewClient(srv.URL, srv.Client()).Send(context.Background(), s.cfg(), "x")
	var ae *APIError
	s.Require().ErrorAs(err, &ae)
	s.Equal(400, ae.Code)
	s.Equal("Bad Request: chat not found", ae.Description)
	s.Equal("Bad Request: chat not found", ae.Error())

	srv2 := s.server(429,
		`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`, nil)
	defer srv2.Close()
	err = NewClient(srv2.URL, srv2.Client()).Send(context.Background(), s.cfg(), "x")
	s.Require().ErrorAs(err, &ae)
	s.Equal(7*time.Second, ae.RetryAfter)
}

func (s *clientSuite) TestNonJSONBody() {
	srv := s.server(502, `<html>Bad Gateway</html>`, nil)
	defer srv.Close()
	err := NewClient(srv.URL, srv.Client()).Send(context.Background(), s.cfg(), "x")
	s.Require().Error(err)
	var ae *APIError
	s.NotErrorAs(err, &ae)
	s.Contains(err.Error(), "502")
	ok, _ := retryable(err)
	s.True(ok)
}

func (s *clientSuite) TestTransportErrorRedacted() {
	srv := s.server(200, `{"ok":true}`, nil)
	url := srv.URL
	srv.Close()
	err := NewClient(url, nil).Send(context.Background(), s.cfg(), "x")
	s.Require().Error(err)
	s.NotContains(err.Error(), testToken)
	s.NotContains(err.Error(), "/bot")
	ok, _ := retryable(err)
	s.True(ok)
}

func (s *clientSuite) TestDescriptionRedacted() {
	body := fmt.Sprintf(`{"ok":false,"error_code":400,"description":"Not Found: /bot%s/sendMessage"}`, testToken)
	srv := s.server(400, body, nil)
	defer srv.Close()
	err := NewClient(srv.URL, srv.Client()).Send(context.Background(), s.cfg(), "x")
	var ae *APIError
	s.Require().ErrorAs(err, &ae)
	s.Contains(ae.Description, "<redacted>")
	s.NotContains(ae.Description, testToken)
	s.Contains(ae.Error(), "<redacted>")
	s.NotContains(ae.Error(), testToken)
}

func (s *clientSuite) TestRetryable() {
	cases := []struct {
		name  string
		err   error
		ok    bool
		after time.Duration
	}{
		{"429", &APIError{Code: 429, RetryAfter: 7 * time.Second}, true, 7 * time.Second},
		{"500", &APIError{Code: 500}, true, 0},
		{"503", &APIError{Code: 503}, true, 0},
		{"400", &APIError{Code: 400}, false, 0},
		{"401", &APIError{Code: 401}, false, 0},
		{"403", &APIError{Code: 403}, false, 0},
		{"404", &APIError{Code: 404}, false, 0},
		{"transport", errors.New("connection refused"), true, 0},
		{"deadline", context.DeadlineExceeded, true, 0},
	}
	for _, c := range cases {
		ok, after := retryable(c.err)
		s.Equal(c.ok, ok, c.name)
		s.Equal(c.after, after, c.name)
	}
}
