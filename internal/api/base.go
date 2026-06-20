package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// base carries the response helpers shared by every api handler. Each handler
// embeds it, so call sites stay `h.writeJSON(...)` / `h.writeError(...)`.
type base struct {
	log *slog.Logger
}

func (b base) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		b.log.Error("encode response", slog.Any("error", err))
	}
}

func (b base) writeError(w http.ResponseWriter, status int, msg string) {
	b.writeJSON(w, status, map[string]string{"error": msg})
}
