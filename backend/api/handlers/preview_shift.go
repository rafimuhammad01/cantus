package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

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
	processor services.ProcessorClient,
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
		serveName := ""
		servePath := ""

		stemCacheHas, err := storage.Has(ctx, videoID, stemShiftedName)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (stem-shifted) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if stemCacheHas {
			p, pathErr := storage.LocalPath(ctx, videoID, stemShiftedName)
			if pathErr != nil {
				log.Error().Err(pathErr).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath (stem-shifted) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}
			servePath = p
			serveName = stemShiftedName
		}

		// Cache miss on stem-shifted. Check stem WAV BEFORE consulting legacy cache.
		if servePath == "" {
			stemWAVHas, err := storage.Has(ctx, videoID, "preview-stems/no_vocals.wav")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (stem WAV) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
				return
			}

			if stemWAVHas {
				// Stem path: shift the clean instrumental stem.
				inputPath, err := storage.LocalPath(ctx, videoID, "preview-stems/no_vocals.wav")
				if err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath (stem WAV) failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
					return
				}

				tmpDir, err := os.MkdirTemp("", "cantus-shift-stem-*")
				if err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("os.MkdirTemp failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
					return
				}
				defer func() { _ = os.RemoveAll(tmpDir) }()

				outputPath := filepath.Join(tmpDir, "shifted.mp3")

				if err := processor.Shift(ctx, inputPath, outputPath, float64(n)); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("processor.Shift failed")
					writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
					return
				}

				if err := storage.Commit(ctx, videoID, stemShiftedName, outputPath); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Commit (stem-shifted) failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage commit failed"})
					return
				}

				p, pathErr := storage.LocalPath(ctx, videoID, stemShiftedName)
				if pathErr != nil {
					log.Error().Err(pathErr).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath (stem-shifted serve) failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
					return
				}
				servePath = p
				serveName = stemShiftedName
			} else {
				// No stem WAV — try the legacy cache (acceptable: predates this feature).
				legacyCacheHas, err := storage.Has(ctx, videoID, legacyShiftedName)
				if err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Has (legacy-shifted) failed")
					writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
					return
				}
				if legacyCacheHas {
					p, pathErr := storage.LocalPath(ctx, videoID, legacyShiftedName)
					if pathErr != nil {
						log.Error().Err(pathErr).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath (legacy-shifted) failed")
						writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
						return
					}
					servePath = p
					serveName = legacyShiftedName
				}
			}
		}

		// Final fallback: still nothing? Run the legacy compute path (download + shift full mix).
		if servePath == "" {
			previewHas, err := storage.Has(ctx, videoID, "preview.mp3")
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

			inputPath, err := storage.LocalPath(ctx, videoID, "preview.mp3")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}

			tmpDir, err := os.MkdirTemp("", "cantus-shift-*")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("os.MkdirTemp failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			outputPath := filepath.Join(tmpDir, "shifted.mp3")

			if err := processor.Shift(ctx, inputPath, outputPath, float64(n)); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("processor.Shift failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
				return
			}

			if err := storage.Commit(ctx, videoID, legacyShiftedName, outputPath); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("storage.Commit failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage commit failed"})
				return
			}

			p, pathErr := storage.LocalPath(ctx, videoID, legacyShiftedName)
			if pathErr != nil {
				log.Error().Err(pathErr).Str("videoId", videoID).Int("semitones", n).Msg("storage.LocalPath (legacy serve) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}
			servePath = p
			serveName = legacyShiftedName
		}

		f, err := os.Open(servePath)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("os.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", n).Msg("file.Stat failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		http.ServeContent(w, r, serveName, info.ModTime(), f)
	}
}
