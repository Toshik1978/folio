package server

import (
	"fmt"
	"log/slog"
	"net/http"
)

type slogger struct {
	*slog.Logger
}

// Print implements the Print method for middleware.Logger.
func (sl *slogger) Print(v ...any) {
	sl.Info(fmt.Sprint(v...))
}

// proxyHeaders normalizes the request scheme from X-Forwarded-Proto (set by the
// fronting proxy / Cloudflare tunnel) so absolute-URL builders can detect
// https. Only the literal values "http" and "https" are honored: on direct
// connections the header is client-controllable (symmetric with the distrusted
// X-Forwarded-Host), and clamping keeps crafted values out of the OpenSearch /
// feed URLs advertised when PUBLIC_URL is unset. It deliberately does NOT honor
// X-Forwarded-Host: because /opds* is reachable without Cloudflare Access, that
// header is client-controllable and trusting it lets a caller poison the
// absolute URLs Folio advertises. The canonical external host comes from config
// (PUBLIC_URL); otherwise the request Host is used as-is.
func proxyHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch proto := r.Header.Get("X-Forwarded-Proto"); {
		case proto == "http" || proto == "https":
			r.URL.Scheme = proto
		case r.TLS != nil:
			r.URL.Scheme = "https"
		default:
			r.URL.Scheme = "http"
		}

		next.ServeHTTP(w, r)
	})
}
