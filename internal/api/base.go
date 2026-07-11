package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// maxJSONBody caps a decoded JSON request body. The metadata/edit payloads are
// small (a few KB at most), so a hostile or accidental huge body is rejected
// rather than read into memory on the low-spec target hosts. This mirrors the
// io.LimitReader cap the cover-upload path already applies to raw image bytes.
const maxJSONBody int64 = 1 << 20 // 1 MiB

// base carries the response helpers shared by every api handler. Each handler
// embeds it, so call sites stay `h.writeJSON(...)` / `h.writeError(...)`.
type base struct {
	log *slog.Logger
}

// decodeJSON decodes a JSON request body into dst, capping the body at
// maxJSONBody via http.MaxBytesReader so an oversized body cannot exhaust
// memory. It returns the decode error (which the caller maps to 400); it does
// not write a response itself.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode json body: %w", err)
	}

	return nil
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
