package handlers

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"

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
		name := "shifted/" + strconv.Itoa(semitones) + "/audio" + services.AudioExt
		key := storage.Key(videoID, name)

		ok, err := storage.Has(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}

		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "audio not generated for this key — call /api/generate first"})
			return
		}

		if url, err := storage.SignGet(ctx, key); err == nil && url != "" {
			http.Redirect(w, r, url, http.StatusFound)
			return
		}

		rc, err := storage.Open(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("storage.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage open failed"})
			return
		}
		defer func() { _ = rc.Close() }()

		// Beta scale; bounded sizes. Streaming Range from R2 can come later if profiling shows memory pressure.
		buf, err := io.ReadAll(rc)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", semitones).Msg("io.ReadAll failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage read failed"})
			return
		}

		http.ServeContent(w, r, "audio"+services.AudioExt, time.Now(), bytes.NewReader(buf))
	}
}
