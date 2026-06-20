package api

import (
	"log/slog"
	"net/http"
)

type facetsResponse struct {
	Formats   []string `json:"formats"`
	Languages []string `json:"languages"`
}

// facets handles GET /api/facets — returns raw distinct formats and languages.
func (h *CatalogHandler) facets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lib := intQueryParam(r, "library")

	formats, err := h.q.ListDistinctFormats(ctx, lib)
	if err != nil {
		h.log.Error("facets formats", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load formats")
		return
	}
	if formats == nil {
		formats = []string{}
	}

	languages, err := h.q.ListDistinctLanguages(ctx, lib)
	if err != nil {
		h.log.Error("facets languages", slog.Any("error", err))
		h.writeError(w, http.StatusInternalServerError, "failed to load languages")
		return
	}
	if languages == nil {
		languages = []string{}
	}

	h.writeJSON(w, http.StatusOK, facetsResponse{
		Formats:   formats,
		Languages: languages,
	})
}
