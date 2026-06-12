package handlers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// noteNames is the canonical 12-note chromatic wheel used for key transposition.
var noteNames = []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// transposeKey applies a semitone shift to a key string of the form
// "<NOTE> <major|minor>". Returns "" if key is empty or malformed.
// The double-mod handles negative semitones correctly in Go, where % can be negative.
func transposeKey(key string, semitones int) string {
	if key == "" {
		return ""
	}
	parts := strings.SplitN(key, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	idx := -1
	for i, n := range noteNames {
		if n == parts[0] {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ""
	}
	newIdx := ((idx+semitones)%12 + 12) % 12
	return noteNames[newIdx] + " " + parts[1]
}

// loadPreviewKey returns the cached preview-key value for videoID, or "" if absent or malformed.
func loadPreviewKey(ctx context.Context, storage services.Storage, videoID string) string {
	ok, err := storage.Has(ctx, videoID, "preview-key.json")
	if err != nil || !ok {
		return ""
	}
	path, err := storage.LocalPath(ctx, videoID, "preview-key.json")
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pk struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &pk); err != nil {
		return ""
	}
	return pk.Key
}

// melodyJSON is the on-disk and on-wire shape of a melody payload.
type melodyJSON struct {
	HopMs         int          `json:"hop_ms"`
	MinHz         float64      `json:"min_hz"`
	MaxHz         float64      `json:"max_hz"`
	Key           string       `json:"key"`
	TransposedKey string       `json:"transposed_key"`
	Frames        [][2]float64 `json:"frames"`
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

		// Override key with preview-key.json when present so the UI shows the
		// same key in both /preview and /play views. The preview-key detector
		// (chroma on full mix) and the melody detector (Krumhansl on isolated
		// vocals) can disagree on enharmonic equivalents (e.g. F major vs A minor);
		// preview computes first and the user sees it first, so it wins.
		key := payload.Key
		if previewKey := loadPreviewKey(ctx, storage, videoID); previewKey != "" {
			key = previewKey
		}

		writeJSON(w, http.StatusOK, transposeMelody(payload, semitones, key))
	}
}

// transposeMelody returns a new melodyJSON with hz values scaled by 2^(semitones/12),
// the key transposed via transposeKey, and unvoiced frames (hz==0) preserved as zero.
func transposeMelody(payload melodyJSON, semitones int, key string) melodyJSON {
	ratio := math.Pow(2, float64(semitones)/12)
	out := melodyJSON{
		HopMs:         payload.HopMs,
		MinHz:         payload.MinHz * ratio,
		MaxHz:         payload.MaxHz * ratio,
		Key:           key,
		TransposedKey: transposeKey(key, semitones),
		Frames:        make([][2]float64, len(payload.Frames)),
	}
	for i, frame := range payload.Frames {
		tMs := frame[0]
		hz := frame[1]
		if hz != 0.0 {
			hz = hz * ratio
		}
		out.Frames[i] = [2]float64{tMs, hz}
	}
	return out
}
