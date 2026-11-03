package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/notify"
)

// TelegramService is the notifier surface the Telegram settings endpoints drive.
// *notify.Service satisfies it.
type TelegramService interface {
	TelegramSettings(ctx context.Context) (notify.Settings, error)
	UpdateTelegram(ctx context.Context, u notify.Update) (notify.Settings, error)
	SendTest(ctx context.Context) error
	DeliveryStatus() notify.DeliveryStatus
}

// TelegramHandler serves the /settings/telegram endpoints.
type TelegramHandler struct {
	log *slog.Logger
	svc TelegramService
}

// NewTelegram builds the Telegram settings handler over a TelegramService.
func NewTelegram(log *slog.Logger, svc TelegramService) *TelegramHandler {
	return &TelegramHandler{log: log, svc: svc}
}

// Register mounts the Telegram settings routes on r.
func (h *TelegramHandler) Register(r chi.Router) {
	r.Get("/settings/telegram", h.get)
	r.Put("/settings/telegram", h.update)
	r.Post("/settings/telegram/test", h.test)
	r.Get("/settings/telegram/status", h.status)
}

type statusView struct {
	LastSuccessAt    *string `json:"last_success_at"`
	LastFailureAt    *string `json:"last_failure_at"`
	LastFailureError string  `json:"last_failure_error"`
}

type telegramView struct {
	Enabled    bool       `json:"enabled"`
	TokenSet   bool       `json:"token_set"`
	TokenLast4 string     `json:"token_last4"`
	ChatID     string     `json:"chat_id"`
	TopicID    *int64     `json:"topic_id"`
	Silent     bool       `json:"silent"`
	Status     statusView `json:"status"`
}

type telegramUpdateRequest struct {
	Enabled  bool    `json:"enabled"`
	BotToken *string `json:"bot_token"` // write-only; absent, null or "" keeps the stored one
	ChatID   string  `json:"chat_id"`
	TopicID  *int64  `json:"topic_id"`
	Silent   bool    `json:"silent"`
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toStatusView(st notify.DeliveryStatus) statusView {
	return statusView{
		LastSuccessAt:    formatTime(st.LastSuccessAt),
		LastFailureAt:    formatTime(st.LastFailureAt),
		LastFailureError: st.LastFailureError,
	}
}

func toTelegramView(s notify.Settings) telegramView {
	return telegramView{
		Enabled:    s.Enabled,
		TokenSet:   s.TokenSet,
		TokenLast4: s.TokenLast4,
		ChatID:     s.ChatID,
		TopicID:    s.TopicID,
		Silent:     s.Silent,
		Status:     toStatusView(s.Status),
	}
}

// get handles GET /settings/telegram.
func (h *TelegramHandler) get(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.TelegramSettings(r.Context())
	if err != nil {
		h.log.Error("read telegram settings", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to read telegram settings")
		return
	}
	h.writeJSON(w, http.StatusOK, toTelegramView(s))
}

// update handles PUT /settings/telegram.
func (h *TelegramHandler) update(w http.ResponseWriter, r *http.Request) {
	var req telegramUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.BotToken != nil && *req.BotToken == "" {
		req.BotToken = nil
	}
	s, err := h.svc.UpdateTelegram(r.Context(), notify.Update{
		Enabled:  req.Enabled,
		BotToken: req.BotToken,
		ChatID:   req.ChatID,
		TopicID:  req.TopicID,
		Silent:   req.Silent,
	})
	if err != nil {
		if ve, ok := errors.AsType[*notify.ValidationError](err); ok {
			h.writeError(w, http.StatusBadRequest, ve.Error())
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.writeError(w, http.StatusServiceUnavailable, "busy, try again")
			return
		}
		h.log.Error("save telegram settings", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to save telegram settings")

		return
	}
	h.writeJSON(w, http.StatusOK, toTelegramView(s))
}

// test handles POST /settings/telegram/test.
func (h *TelegramHandler) test(w http.ResponseWriter, r *http.Request) {
	err := h.svc.SendTest(r.Context())
	switch {
	case err == nil:
		h.writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": toStatusView(h.svc.DeliveryStatus()),
		})
	case errors.Is(err, notify.ErrNotConfigured):
		h.writeError(w, http.StatusConflict, "save a bot token and a channel ID first")
	case errors.Is(err, context.DeadlineExceeded):
		h.writeError(w, http.StatusServiceUnavailable, "busy, try again")
	default:
		// A delivery failure is Warn, never Error: it must not reach the error window.
		h.log.Warn("telegram test send failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusBadGateway, map[string]any{
			errorKey: err.Error(),
			"status": toStatusView(h.svc.DeliveryStatus()),
		})
	}
}

// status handles GET /settings/telegram/status.
func (h *TelegramHandler) status(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, toStatusView(h.svc.DeliveryStatus()))
}

func (h *TelegramHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error("encode response", slog.Any("error", err))
	}
}

func (h *TelegramHandler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]string{"error": msg})
}
