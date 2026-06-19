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

// Preview returns an http.HandlerFunc that serves a 30-second audio preview for a videoID.
func Preview(signer *services.Signer, storage services.Storage, svc services.YouTubeService) http.HandlerFunc {
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

		log := logger.FromCtx(r.Context())
		key := storage.Key(videoID, "preview.mp3")

		ok, err := storage.Has(r.Context(), key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}

		if !ok {
			if err := svc.DownloadPreview(r.Context(), videoID); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("DownloadPreview failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "download failed"})
				return
			}
		}

		if url, err := storage.SignGet(r.Context(), key); err == nil && url != "" {
			http.Redirect(w, r, url, http.StatusFound)
			return
		}

		rc, err := storage.Open(r.Context(), key)
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

		http.ServeContent(w, r, "preview.mp3", time.Now(), bytes.NewReader(buf))
	}
}
