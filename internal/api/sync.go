package api

import "net/http"

// triggerSync handles POST /api/sync — force a re-index of all libraries,
// bypassing checkpoint gating (manual triggers always force).
func (h *SyncHandler) triggerSync(w http.ResponseWriter, _ *http.Request) {
	h.sync.TriggerAllForced()
	h.writeJSON(w, http.StatusAccepted, h.sync.Status())
}

// triggerDeepIndex handles POST /api/deep-index — queue a Deep Index run for
// every library that is not pending purge.
func (h *SyncHandler) triggerDeepIndex(w http.ResponseWriter, _ *http.Request) {
	h.sync.TriggerDeepIndexAll()
	h.writeJSON(w, http.StatusAccepted, h.sync.Status())
}

// syncStatus handles GET /api/sync/status — current engine state.
func (h *SyncHandler) syncStatus(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, h.sync.Status())
}
