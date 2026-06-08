package handlers

import (
	"net/http"
	"strconv"

	"cantus/backend/logger"
	"cantus/backend/models"
	"cantus/backend/services"
)

// searchResponse is the JSON shape returned by the Search handler.
type searchResponse struct {
	Items   []models.SearchResult `json:"items"`
	HasMore bool                  `json:"has_more"`
}

// errorResponse is the JSON shape returned on handler errors.
type errorResponse struct {
	Error string `json:"error"`
}

// Search returns an http.HandlerFunc that proxies /api/songs/search to the YouTubeService.
func Search(svc services.YouTubeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "q is required"})
			return
		}
		if len(q) > 200 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "q must be 1..200 characters"})
			return
		}

		limit := 10
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "limit must be a number"})
				return
			}
			limit = n
		}
		if limit < 1 || limit > 20 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "limit must be 1..20"})
			return
		}

		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "offset must be a number"})
				return
			}
			offset = n
		}
		if offset < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "offset must be >= 0"})
			return
		}

		page, err := svc.Search(r.Context(), q, limit, offset)
		if err != nil {
			log := logger.FromCtx(r.Context())
			log.Error().Err(err).Msg("upstream search failed")
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "upstream search failed"})
			return
		}

		items := page.Items
		if items == nil {
			items = make([]models.SearchResult, 0)
		}

		writeJSON(w, http.StatusOK, searchResponse{Items: items, HasMore: page.HasMore})
	}
}
