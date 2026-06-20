package api

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/Toshik1978/folio/internal/events"
	"github.com/Toshik1978/folio/internal/sync"
)

// SyncEngine is the subset of *sync.Engine the API drives. It is an interface
// so handlers can be tested with a lightweight fake.
type SyncEngine interface {
	TriggerAll()
	TriggerAllForced()
	TriggerLibrary(libraryID int64)
	TriggerLibraryForced(libraryID int64)
	Status() sync.Status
	Reschedule(ctx context.Context) error
	RequestPurge(id int64)
}

// EventBroker is the subset of *events.Broker the SSE handler uses. It is an
// interface so the dependency can be faked in tests. *events.Broker satisfies it.
type EventBroker interface {
	Subscribe() (*events.Subscription, bool)
	Unsubscribe(sub *events.Subscription)
}

// SyncHandler serves the sync trigger/status endpoints and the SSE event stream.
type SyncHandler struct {
	base

	sync   SyncEngine
	events EventBroker // optional; nil disables the SSE stream
}

// NewSync builds the sync handler; broker may be nil to disable the SSE stream.
func NewSync(log *slog.Logger, syncEngine SyncEngine, broker EventBroker) *SyncHandler {
	return &SyncHandler{base: base{log: log}, sync: syncEngine, events: broker}
}

func (h *SyncHandler) Register(r chi.Router) {
	r.Post("/sync", h.triggerSync)
	r.Get("/sync/status", h.syncStatus)
	r.Get("/sync/events", h.syncEvents)
}
