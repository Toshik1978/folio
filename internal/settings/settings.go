// Package settings serves the /api/settings endpoints. It is a thin HTTP adapter
// over the auth credentials service and holds no persistence of its own.
package settings

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Service is the credential store the settings endpoints drive.
// *auth.Authenticator satisfies it.
type Service interface {
	View(ctx context.Context) (user string, set bool, err error)
	SetCredentials(ctx context.Context, user, pass *string) error
}

// Handler serves GET/PUT /settings.
type Handler struct {
	log  *slog.Logger
	auth Service
}

// New builds the settings handler over a credentials Service.
func New(log *slog.Logger, auth Service) *Handler {
	return &Handler{log: log, auth: auth}
}

// Register mounts the settings routes on r.
func (h *Handler) Register(r chi.Router) {
	r.Get("/settings", h.get)
	r.Put("/settings", h.update)
}

type view struct {
	OPDSUser    string `json:"opds_user"`
	OPDSPassSet bool   `json:"opds_pass_set"`
}

type updateRequest struct {
	OPDSUser *string `json:"opds_user"`
	OPDSPass *string `json:"opds_pass"` // write-only plaintext; stored hashed
}

// get handles GET /settings. The password is write-only: only whether one is set
// is reported.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	user, set, err := h.auth.View(r.Context())
	if err != nil {
		h.log.Error("read settings", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	h.writeJSON(w, http.StatusOK, view{OPDSUser: user, OPDSPassSet: set})
}

// update handles PUT /settings. Only provided fields are changed.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.auth.SetCredentials(r.Context(), req.OPDSUser, req.OPDSPass); err != nil {
		h.log.Error("save settings", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to save settings")
		return
	}

	user, set, err := h.auth.View(r.Context())
	if err != nil {
		h.log.Error("read settings", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	h.writeJSON(w, http.StatusOK, view{OPDSUser: user, OPDSPassSet: set})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error("encode response", slog.Any("error", err))
	}
}

func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]string{"error": msg})
}
