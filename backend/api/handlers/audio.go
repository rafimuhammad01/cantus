package handlers

import (
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// Audio returns an http.HandlerFunc that serves the cached pitch-shifted full instrumental MP3.
func Audio(signer *services.Signer, storage services.Storage) http.HandlerFunc {
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
		name := "shifted/" + strconv.Itoa(semitones) + "/audio.mp3"

		ok, err := storage.Has(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}

		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "audio not generated for this key — call /api/generate first"})
			return
		}

		path, err := storage.LocalPath(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("storage.LocalPath failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		f, err := os.Open(path)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("os.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("file.Stat failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		http.ServeContent(w, r, "audio.mp3", info.ModTime(), f)
	}
}
