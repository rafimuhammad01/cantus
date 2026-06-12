package handlers

import (
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// PreviewAudio returns an http.HandlerFunc that serves the cached preview-stems
// no_vocals.mp3 (original-key, 30s). It takes no semitones param because the
// preview audio is always the unshifted stem; shifted previews are served by the
// existing /api/preview-shift endpoint. No auto-generate: /api/preview-stems is
// the entry point and the frontend always calls it before this.
func PreviewAudio(signer *services.Signer, storage services.Storage) http.HandlerFunc {
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

		const name = "preview-stems/no_vocals.mp3"

		ok, err := storage.Has(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !ok {
			// 404-not-auto-generate: preview-stems is a required prerequisite that
			// the frontend explicitly triggers; returning 404 makes that contract clear.
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "preview not generated — call /api/preview-stems first"})
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

		info, err := f.Stat()
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("file.Stat failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		http.ServeContent(w, r, "no_vocals.mp3", info.ModTime(), f)
	}
}
