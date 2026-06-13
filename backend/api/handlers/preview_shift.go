package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"cantus/backend/logger"
	"cantus/backend/services"
)

type previewShiftRequest struct {
	VideoID   string `json:"video_id"`
	Sig       string `json:"sig"`
	Semitones int    `json:"semitones"`
}

// PreviewShift returns an http.HandlerFunc that serves a pitch-shifted 30s preview.
func PreviewShift(
	signer *services.Signer,
	storage services.Storage,
	ytSvc services.YouTubeService,
	cpu services.CPUProcessorClient,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req previewShiftRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
			return
		}

		if !services.ValidVideoID(req.VideoID) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid videoId"})
			return
		}

		if !signer.Valid(req.VideoID, req.Sig) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid sig"})
			return
		}

		if req.Semitones < -12 || req.Semitones > 12 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "semitones must be in [-12, 12]"})
			return
		}

		ctx := r.Context()
		log := logger.FromCtx(ctx)
		videoID := req.VideoID
		n := req.Semitones

		stemShiftedName := "preview-stems/shifted/" + strconv.Itoa(n) + ".mp3"
		legacyShiftedName := "preview-shifts/" + strconv.Itoa(n) + ".mp3"

		// Resolution order is load-bearing for the "no chipmunk" promise:
		//   1. stem-shifted cache hit  → serve (clean, previously computed)
		//   2. stem WAV present        → compute clean shift, never touch legacy
		//   3. legacy-shifted cache    → serve (only valid if stem WAV is absent)
		//   4. legacy compute          → download preview, shift full mix
		// Step 3 must come after step 2: a song with both a stem WAV AND a stale
		// legacy-shifted from before this feature shipped would otherwise serve
		// the chipmunky legacy file instead of recomputing on the clean stem.
		serveKey := ""
		serveName := ""

		stemCacheHas, err := storage.Has(ctx, storage.Key(videoID, stemShiftedName))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (stem-shifted) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if stemCacheHas {
			serveKey = storage.Key(videoID, stemShiftedName)
			serveName = stemShiftedName
		}

		// Cache miss on stem-shifted. Check stem WAV BEFORE consulting legacy cache.
		if serveKey == "" {
			stemWAVHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals.wav"))
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (stem WAV) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
				return
			}

			if stemWAVHas {
				// Stem path: shift the clean instrumental stem via URL handoff.
				inKey := storage.Key(videoID, "preview-stems/no_vocals.wav")
				outKey := storage.Key(videoID, stemShiftedName)
				inURL, err := storage.SignGet(ctx, inKey)
				if err != nil {
					log.Error().Err(err).Msg("storage.SignGet failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign get failed"})
					return
				}
				outURL, err := storage.SignPut(ctx, outKey)
				if err != nil {
					log.Error().Err(err).Msg("storage.SignPut failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
					return
				}
				if err := cpu.Shift(ctx, inURL, outURL, float64(n)); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("processor.Shift failed")
					writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
					return
				}
				if err := storage.Verify(ctx, outKey); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Verify failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "shift output not materialized"})
					return
				}
				serveKey = outKey
				serveName = stemShiftedName
			} else {
				// No stem WAV — try the legacy cache (acceptable: predates this feature).
				legacyCacheHas, err := storage.Has(ctx, storage.Key(videoID, legacyShiftedName))
				if err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (legacy-shifted) failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
					return
				}
				if legacyCacheHas {
					serveKey = storage.Key(videoID, legacyShiftedName)
					serveName = legacyShiftedName
				}
			}
		}

		// Final fallback: still nothing? Run the legacy compute path (download + shift full mix).
		if serveKey == "" {
			previewHas, err := storage.Has(ctx, storage.Key(videoID, "preview.mp3"))
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (preview) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
				return
			}

			if !previewHas {
				if err := ytSvc.DownloadPreview(ctx, videoID); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("DownloadPreview failed")
					writeJSON(w, http.StatusBadGateway, errorResponse{Error: "download failed"})
					return
				}
			}

			inKey := storage.Key(videoID, "preview.mp3")
			outKey := storage.Key(videoID, legacyShiftedName)
			inURL, err := storage.SignGet(ctx, inKey)
			if err != nil {
				log.Error().Err(err).Msg("storage.SignGet failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign get failed"})
				return
			}
			outURL, err := storage.SignPut(ctx, outKey)
			if err != nil {
				log.Error().Err(err).Msg("storage.SignPut failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
				return
			}
			if err := cpu.Shift(ctx, inURL, outURL, float64(n)); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("processor.Shift failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
				return
			}
			if err := storage.Verify(ctx, outKey); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Verify failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "shift output not materialized"})
				return
			}
			serveKey = outKey
			serveName = legacyShiftedName
		}

		rc, err := storage.Open(ctx, serveKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage open failed"})
			return
		}
		defer func() { _ = rc.Close() }()

		// Beta scale; bounded MP3 sizes. Streaming Range from R2 can come later if profiling shows memory pressure.
		buf, err := io.ReadAll(rc)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("io.ReadAll failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage read failed"})
			return
		}

		http.ServeContent(w, r, serveName, time.Now(), bytes.NewReader(buf))
	}
}
