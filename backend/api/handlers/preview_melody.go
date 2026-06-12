package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// PreviewMelody returns an http.HandlerFunc that serves a math-transposed melody
// built from the preview-stems/melody.json produced by /api/preview-stems.
// It mirrors Melody() but reads from the preview-stems path rather than the
// full-pipeline path, so it is available as soon as preview-stems completes.
func PreviewMelody(signer *services.Signer, storage services.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := chi.URLParam(r, "videoId")
		if !services.ValidVideoID(videoID) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid videoId"})
			return
		}

		raw := chi.URLParam(r, "semitones")
		semitones, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid semitones"})
			return
		}
		if semitones < -12 || semitones > 12 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "semitones must be in [-12, 12]"})
			return
		}

		sig := r.URL.Query().Get("sig")
		if !signer.Valid(videoID, sig) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid sig"})
			return
		}

		ctx := r.Context()
		log := logger.FromCtx(ctx)

		const name = "preview-stems/melody.json"

		ok, err := storage.Has(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "preview melody not generated — call /api/preview-stems first"})
			return
		}

		path, err := storage.LocalPath(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.LocalPath failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		f, err := os.Open(path)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("os.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}
		defer func() { _ = f.Close() }()

		var payload melodyJSON
		if err := json.NewDecoder(f).Decode(&payload); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("melody decode failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "melody parse failed"})
			return
		}

		// Apply the same key-override rule as Melody(): preview-key.json (chroma on
		// full mix) wins over the CREPE key embedded in melody.json when present and
		// non-empty, so both views show a consistent key to the user.
		key := payload.Key
		if previewKey := loadPreviewKey(ctx, storage, videoID); previewKey != "" {
			key = previewKey
		}

		writeJSON(w, http.StatusOK, transposeMelody(payload, semitones, key))
	}
}
