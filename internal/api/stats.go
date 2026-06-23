package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

type statsView struct {
	TotalBooks     int64            `json:"total_books"`
	TotalSizeBytes int64            `json:"total_size_bytes"`
	Authors        int64            `json:"authors"`
	Series         int64            `json:"series"`
	Libraries      int64            `json:"libraries"`
	Formats        map[string]int64 `json:"formats"`
	Languages      map[string]int64 `json:"languages"`
}

// stats handles GET /api/stats — cached global library totals.
func (h *CatalogHandler) stats(w http.ResponseWriter, r *http.Request) {
	h.cacheMutex.Lock()
	if !h.cacheValid {
		stats, err := h.computeStats(r.Context())
		if err != nil {
			h.cacheMutex.Unlock()
			h.statsError(w, err)
			return
		}
		h.cachedStats = stats
		h.cacheValid = true
	}
	stats := h.cachedStats
	h.cacheMutex.Unlock()
	h.writeJSON(w, http.StatusOK, stats)
}

func (h *CatalogHandler) computeStats(ctx context.Context) (statsView, error) {
	if h.computeHook != nil {
		h.computeHook()
	}
	global, err := h.q.GlobalStats(ctx)
	if err != nil {
		return statsView{}, fmt.Errorf("get global stats: %w", err)
	}

	formatRows, err := h.q.GlobalBooksByFormat(ctx)
	if err != nil {
		return statsView{}, fmt.Errorf("get global books: %w", err)
	}
	formats := make(map[string]int64, len(formatRows))
	for i := range formatRows {
		formats[formatRows[i].FileFormat] = formatRows[i].BookCount
	}

	langRows, err := h.q.GlobalBooksByLanguage(ctx)
	if err != nil {
		return statsView{}, fmt.Errorf("get global books: %w", err)
	}
	languages := make(map[string]int64, len(langRows))
	for i := range langRows {
		languages[langRows[i].Language] = langRows[i].BookCount
	}

	return statsView{
		TotalBooks:     global.TotalBooks,
		TotalSizeBytes: global.TotalSizeBytes,
		Authors:        global.Authors,
		Series:         global.Series,
		Libraries:      global.Libraries,
		Formats:        formats,
		Languages:      languages,
	}, nil
}

func (h *CatalogHandler) statsError(w http.ResponseWriter, err error) {
	h.log.Error("stats", slog.Any("error", err))
	h.writeError(w, http.StatusInternalServerError, "failed to compute stats")
}
