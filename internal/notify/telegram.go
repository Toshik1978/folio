package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes caps how much of a Telegram response is read.
const maxResponseBytes = 1 << 20

// Client talks to the Telegram Bot API.
type Client struct {
	http *http.Client
	base string
}

// NewClient returns a client for the Bot API at base (for example
// https://api.telegram.org). A nil httpClient uses a client with a 10 s timeout.
func NewClient(base string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{http: httpClient, base: strings.TrimRight(base, "/")}
}

// APIError is an error reported by Telegram itself (ok: false).
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string { return e.Description }

type sendRequest struct {
	ChatID              string             `json:"chat_id"`
	Text                string             `json:"text"`
	MessageThreadID     int64              `json:"message_thread_id,omitempty"`
	DisableNotification bool               `json:"disable_notification"`
	LinkPreviewOptions  linkPreviewOptions `json:"link_preview_options"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send posts text to the configured chat. No returned error contains the bot
// token.
func (c *Client) Send(ctx context.Context, cfg Config, text string) error {
	payload, err := json.Marshal(sendRequest{
		ChatID:              cfg.ChatID,
		Text:                text,
		MessageThreadID:     cfg.TopicID,
		DisableNotification: cfg.Silent,
		LinkPreviewOptions:  linkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		return redact(err, cfg.BotToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/bot"+cfg.BotToken+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return redact(err, cfg.BotToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return redact(err, cfg.BotToken)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return redact(err, cfg.BotToken)
	}
	var r apiResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return redact(fmt.Errorf("telegram: unexpected response (HTTP %d)", resp.StatusCode), cfg.BotToken)
	}
	if r.OK {
		return nil
	}
	code := r.ErrorCode
	if code == 0 {
		code = resp.StatusCode
	}

	return &APIError{
		Code:        code,
		Description: strings.ReplaceAll(r.Description, cfg.BotToken, "<redacted>"),
		RetryAfter:  time.Duration(r.Parameters.RetryAfter) * time.Second,
	}
}

// redact unwraps a *url.Error to its inner error and replaces every
// occurrence of token in the message with <redacted>.
func redact(err error, token string) error {
	if err == nil {
		return nil
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}

	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}

	return errors.New(strings.ReplaceAll(err.Error(), token, "<redacted>"))
}

// retryable reports whether a failed Send is worth retrying and, for rate
// limiting, how long Telegram asked to wait.
func retryable(err error) (bool, time.Duration) {
	if ae, ok := errors.AsType[*APIError](err); ok {
		switch {
		case ae.Code == http.StatusTooManyRequests:
			return true, ae.RetryAfter
		case ae.Code >= 500:
			return true, 0
		default:
			return false, 0
		}
	}

	return true, 0
}
