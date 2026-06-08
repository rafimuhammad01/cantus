package handlers

import (
	"net/http"
	"os"

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

		ok, err := storage.Has(r.Context(), videoID, "preview.mp3")
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

		path, err := storage.LocalPath(r.Context(), videoID, "preview.mp3")
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

		http.ServeContent(w, r, "preview.mp3", info.ModTime(), f)
	}
}
