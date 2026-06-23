package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Toshik1978/folio/internal/events"
)

// sseHeartbeat is the keepalive-ping interval. It keeps intermediaries from
// idle-timing-out the stream (comfortably under the server IdleTimeout of 120s),
// surfaces a dead connection via a failed write, and — because the ping is a
// real named event (see writePing) — lets the client's silence watchdog tell an
// idle-but-healthy stream apart from a wedged one. The client's watchdog must be
// set above this interval (see WATCHDOG_MS in useSyncStatus.ts).
const sseHeartbeat = 20 * time.Second

// syncEvents handles GET /api/sync/events — a Server-Sent Events stream of sync
// state (status/library/progress). The browser's EventSource auto-reconnects; the
// frontend falls back to polling /sync/status if the stream never delivers.
func (h *SyncHandler) syncEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming Unsupported", http.StatusInternalServerError)
		return
	}
	if h.events == nil {
		http.Error(w, "Events Unavailable", http.StatusServiceUnavailable)
		return
	}
	sub, ok := h.events.Subscribe()
	if !ok {
		http.Error(w, "Too Many Subscribers", http.StatusServiceUnavailable)
		return
	}
	defer h.events.Unsubscribe(sub)

	// Long-lived stream: clear the per-write deadline (WriteTimeout, 60s) the same
	// way book downloads do — see internal/bookfile/bookfile.go.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	flusher.Flush()

	// Initial snapshot so a fresh client need not wait for the next transition.
	if err := writeEvent(w, events.Event{Type: events.TypeStatus, Data: h.sync.Status()}); err != nil {
		return
	}
	flusher.Flush()

	h.sseHandler(w, r, sub)
}

func (h *SyncHandler) sseHandler(w http.ResponseWriter, r *http.Request, sub *events.Subscription) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming Unsupported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case <-ticker.C:
			if err := writePing(w); err != nil {
				return
			}
			flusher.Flush()
		case <-sub.C():
			for _, ev := range sub.Drain() {
				if err := writeEvent(w, ev); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}

// writePing emits the heartbeat as a real, named SSE event rather than a bare
// comment (": ping"). A comment keeps proxies from idle-timing out the stream
// but is invisible to the browser's EventSource, so the client cannot use it as
// a liveness signal — it would mistake an idle-but-healthy stream for a dead one
// and reconnect on a loop. A named "ping" event is delivered to a client
// listener instead. The data: line is required and must be non-empty: EventSource
// discards a frame whose data buffer is empty without dispatching it.
func writePing(w io.Writer) error {
	if _, err := io.WriteString(w, "event: ping\ndata: {}\n\n"); err != nil {
		return fmt.Errorf("failed to write ping: %w", err)
	}

	return nil
}

// writeEvent serializes one event as an SSE frame: `event: <type>\n` +
// `data: <json>\n\n`. json.Marshal emits a single line, so one data: line suffices.
func writeEvent(w io.Writer, ev events.Event) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	return nil
}
