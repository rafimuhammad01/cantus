package handlers

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// previewKeyResponse is the JSON shape returned by the PreviewKey handler.
type previewKeyResponse struct {
	Key string `json:"key"`
}

// PreviewKey returns an http.HandlerFunc that returns the song's key for the UI
// to display on PreviewView. There is one accurate detector in the system —
// CREPE on the full isolated vocals stem, stored in melody.json. This handler
// just re-exposes melody.json's key when present, returning "" otherwise so
// the UI can hide the label until /api/generate has produced melody.json.
//
// ytSvc and processor are kept in the signature for API stability with the
// router wiring; they're no longer used because no chroma estimation is run.
func PreviewKey(
	signer *services.Signer,
	storage services.Storage,
	_ services.YouTubeService,
	_ services.ProcessorClient,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := chi.URLParam(r, "videoId")
		if !services.ValidVideoID(videoID) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid videoId"})
			return
		}

		sig := r.URL.Query().Get("sig")
		if !signer.Valid(videoID, sig) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid sig"})
			return
		}

		ctx := r.Context()
		log := logger.FromCtx(ctx)

		ok, err := storage.Has(ctx, videoID, "melody.json")
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (melody.json) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, previewKeyResponse{Key: ""})
			return
		}

		path, err := storage.LocalPath(ctx, videoID, "melody.json")
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.LocalPath (melody.json) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("os.ReadFile (melody.json) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "melody read failed"})
			return
		}

		var payload struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("melody.json decode failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "melody parse failed"})
			return
		}

		writeJSON(w, http.StatusOK, previewKeyResponse{Key: payload.Key})
	}
}
