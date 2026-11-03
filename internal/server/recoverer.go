package server

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/logging"
)

// CrashReporter receives recovered handler panics. A nil CrashReporter means
// log only. ReportCrash reports whether the crash was queued as a message.
type CrashReporter interface {
	ReportCrash(place, detail string, recovered any, stack []byte) bool
}

// recoverer recovers handler panics: it logs them at Error (marked reported, so
// the log tap does not notify a second time), hands them to crashes when
// non-nil, and answers 500 unless the connection is being upgraded. Only the
// chi route pattern is logged or reported, never the raw path or query. A panic
// with http.ErrAbortHandler (directly or wrapped) is re-raised as the bare
// sentinel: net/http compares it by equality, so only that aborts the response
// quietly. The ResponseWriter is not wrapped, so
// SSE flushing is unaffected.
func recoverer(log *slog.Logger, crashes CrashReporter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(http.ErrAbortHandler)
				}

				route := ""
				if rc := chi.RouteContext(r.Context()); rc != nil {
					route = rc.RoutePattern()
				}
				stack := debug.Stack()
				log.ErrorContext(logging.MarkReported(r.Context()), "http handler panicked",
					"method", r.Method, "route", route, "panic", v, "stack", string(stack))
				if crashes != nil {
					crashes.ReportCrash("web request handling", r.Method+" "+route, v, stack)
				}

				if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
