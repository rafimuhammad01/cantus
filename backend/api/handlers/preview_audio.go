package handlers

import (
	"bytes"
	"io"
	"net/http"
	"time"

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

		const name = "preview-stems/no_vocals" + services.AudioExt
		key := storage.Key(videoID, name)

		ok, err := storage.Has(ctx, key)
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

		if url, err := storage.SignGet(ctx, key); err == nil && url != "" {
			http.Redirect(w, r, url, http.StatusFound)
			return
		}

		rc, err := storage.Open(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage open failed"})
			return
		}
		defer func() { _ = rc.Close() }()

		// Beta scale; bounded MP3 sizes. Streaming Range from R2 can come later if profiling shows memory pressure.
		buf, err := io.ReadAll(rc)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("io.ReadAll failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage read failed"})
			return
		}

		http.ServeContent(w, r, "no_vocals"+services.AudioExt, time.Now(), bytes.NewReader(buf))
	}
}
