package handlers

import (
	"encoding/json"
	"math"
	"net/http"
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
		key := storage.Key(videoID, "melody.json")

		ok, err := storage.Has(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "melody not generated — call /api/generate first"})
			return
		}

		rc, err := storage.Open(ctx, key)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage open failed"})
			return
		}
		defer func() { _ = rc.Close() }()

		var payload melodyJSON
		if err := json.NewDecoder(rc).Decode(&payload); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("melody decode failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "melody parse failed"})
			return
		}

		// PlayView uses melody.key directly — CREPE on the full isolated vocals
		// stem is the most accurate detector we have. Chroma on the polyphonic
		// full mix was tried as an override here but produced relative-minor /
		// IV-of confusion (e.g. Dm vs F major, D vs A major) and broke results
		// that CREPE-on-vocals had right. Don't override.
		writeJSON(w, http.StatusOK, transposeMelody(payload, semitones, payload.Key))
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
