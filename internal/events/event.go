// Package events is a transport-agnostic publish/subscribe broker for live
// server-to-client domain events (sync state, indexing progress). It imports
// nothing from sync/api so it stays a leaf package. The SSE framing lives in the
// api package; this package only fans events out to subscribers.
package events

// EventType identifies an event's kind. It is written verbatim as the SSE
// `event:` field, so the frontend can attach typed listeners.
type EventType string

const (
	// TypeStatus carries the engine's full state snapshot (running/current/queued).
	TypeStatus EventType = "status"
	// TypeProgress carries per-library indexing progress (Phase 2).
	TypeProgress EventType = "progress"
	// TypeLibrary signals that one library row materially changed.
	TypeLibrary EventType = "library"
)

// Event is the envelope fanned out to every subscriber. Data is JSON-marshaled
// into the SSE `data:` field; the broker never inspects it.
type Event struct {
	Type EventType
	// CoalesceKey controls slow-consumer handling: "" means the event is reliable
	// (never dropped); a non-empty key means a backed-up subscriber only sees the
	// latest event sharing that key (e.g. "progress:7", "status").
	CoalesceKey string
	Data        any
}
