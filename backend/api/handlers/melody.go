package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// melodyJSON is the on-disk and on-wire shape of a melody payload.
type melodyJSON struct {
	HopMs  int          `json:"hop_ms"`
	MinHz  float64      `json:"min_hz"`
	MaxHz  float64      `json:"max_hz"`
	Frames [][2]float64 `json:"frames"`
}

// Melody returns an http.HandlerFunc that serves a math-transposed melody.json.
func Melody(signer *services.Signer, storage services.Storage) http.HandlerFunc {
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

		ok, err := storage.Has(ctx, videoID, "melody.json")
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "melody not generated — call /api/generate first"})
			return
		}

		path, err := storage.LocalPath(ctx, videoID, "melody.json")
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

		ratio := math.Pow(2, float64(semitones)/12)

		out := melodyJSON{
			HopMs:  payload.HopMs,
			MinHz:  payload.MinHz * ratio,
			MaxHz:  payload.MaxHz * ratio,
			Frames: make([][2]float64, len(payload.Frames)),
		}

		for i, frame := range payload.Frames {
			tMs := frame[0]
			hz := frame[1]
			if hz != 0.0 {
				hz = hz * ratio
			}
			out.Frames[i] = [2]float64{tMs, hz}
		}

		writeJSON(w, http.StatusOK, out)
	}
}
