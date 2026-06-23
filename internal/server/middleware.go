package server

import (
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
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
// stateChanging reports whether m can mutate server state and therefore needs
// the cross-site guard. Safe methods (GET/HEAD/OPTIONS) are exempt.
func stateChanging(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// sameSiteGuard rejects state-changing requests that a browser reports as coming
// from another site, giving token-less CSRF protection that does NOT depend on
// the fronting authenticator's cookie SameSite policy or on the deployment being
// behind Cloudflare Access. It is a deliberate no-op for safe methods and for
// non-browser clients (OPDS readers, curl, CLI), which send neither
// Sec-Fetch-Site nor Origin — so it is a CSRF guard, not authentication.
//
// Decision order for state-changing methods:
//  1. Sec-Fetch-Site (sent by all current browsers): allow same-origin/same-site,
//     allow "none" (user-initiated: typed URL, bookmark — not attacker-reachable),
//     reject "cross-site" and any unrecognized value.
//  2. Fallback for clients without Sec-Fetch-Site: a present Origin must match an
//     allowed origin; an absent Origin means a non-browser caller, which passes.
//
// allowed holds the origins treated as first-party: the configured PublicURL
// plus the request's own scheme+host (which covers direct/LAN access when
// PublicURL is unset).
func sameSiteGuard(publicURL string) func(http.Handler) http.Handler {
	allowed := originOf(publicURL) // "" when PublicURL is unset or unparsable

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !stateChanging(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				switch site {
				case "same-origin", "same-site", "none":
					next.ServeHTTP(w, r)
				default:
					http.Error(w, "403 cross-site request blocked", http.StatusForbidden)
				}

				return
			}

			// No Sec-Fetch-Site. A browser-sent Origin must be first-party; an
			// absent Origin ("") means a non-browser client, which we allow.
			switch origin := r.Header.Get("Origin"); origin {
			case "", allowed, r.URL.Scheme + "://" + r.Host:
				next.ServeHTTP(w, r)
			default:
				http.Error(w, "403 cross-site request blocked", http.StatusForbidden)
			}
		})
	}
}

// originOf returns the scheme://host origin of raw, or "" if it cannot be parsed
// into an absolute origin. Used to normalize PublicURL once at construction.
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}

	return u.Scheme + "://" + u.Host
}

// isFormContentType reports whether media is one of the three media types a
// cross-site "simple request" (an HTML form or a no-preflight fetch) is allowed
// to set. Folio's API speaks JSON and raw bytes, never form encodings, so these
// only ever appear on a forged cross-site write.
func isFormContentType(media string) bool {
	switch media {
	case "application/x-www-form-urlencoded", "multipart/form-data", "text/plain":
		return true
	default:
		return false
	}
}

// formBodyGuard rejects state-changing requests whose body uses a browser form
// content type. It is defense-in-depth behind sameSiteGuard: it closes the
// simple-request CSRF trick in the narrow case where a request carries neither
// Sec-Fetch-Site nor Origin (so sameSiteGuard waves it through) yet smuggles a
// JSON payload under a form content type. Bodyless writes (no Content-Type),
// application/json, and raw image uploads are all unaffected.
func formBodyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stateChanging(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		ct := r.Header.Get("Content-Type")
		if ct == "" {
			next.ServeHTTP(w, r) // bodyless write (e.g. POST .../sync)
			return
		}

		// Strip charset/boundary parameters before matching. An unparsable type is
		// not a known form type, so it falls through to the primary guard.
		if media, _, err := mime.ParseMediaType(ct); err == nil && isFormContentType(media) {
			http.Error(w, "415 form content types are not accepted", http.StatusUnsupportedMediaType)
			return
		}

		next.ServeHTTP(w, r)
	})
}

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
