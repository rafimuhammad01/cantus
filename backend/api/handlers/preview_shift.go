package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// shiftViaStorage downloads inKey to scratch, runs shifter, commits scratch out to outKey,
// then verifies. inExt and outExt determine the tempfile extensions so Shifter's MP3↔WAV
// dispatch picks the right pipeline. Returns early if outKey is already cached.
func shiftViaStorage(
	ctx context.Context,
	storage services.Storage,
	shifter services.Shifter,
	inKey, outKey string,
	inExt, outExt string,
	semitones float64,
) error {
	// Cache check — skip the whole pipeline if the output is already present.
	has, err := storage.Has(ctx, outKey)
	if err != nil {
		return fmt.Errorf("cache check: %w", err)
	}
	if has {
		return nil
	}

	rc, err := storage.Open(ctx, inKey)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() { _ = rc.Close() }()

	scratchIn, err := os.CreateTemp("", "cantus-pshift-in-*"+inExt)
	if err != nil {
		return fmt.Errorf("tempfile in: %w", err)
	}
	defer func() { _ = os.Remove(scratchIn.Name()) }()
	if _, err := io.Copy(scratchIn, rc); err != nil {
		_ = scratchIn.Close()
		return fmt.Errorf("copy input: %w", err)
	}
	_ = scratchIn.Close()

	scratchOut, err := os.CreateTemp("", "cantus-pshift-out-*"+outExt)
	if err != nil {
		return fmt.Errorf("tempfile out: %w", err)
	}
	_ = scratchOut.Close()
	defer func() { _ = os.Remove(scratchOut.Name()) }()

	if err := services.Retry(ctx, services.PipelineRetryAttempts, services.PipelineRetryBaseDelay, func() error {
		return shifter.Shift(ctx, scratchIn.Name(), scratchOut.Name(), semitones)
	}); err != nil {
		return fmt.Errorf("shift: %w", err)
	}
	if err := storage.Commit(ctx, outKey, scratchOut.Name()); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := storage.Verify(ctx, outKey); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return nil
}

// PreviewShift returns an http.HandlerFunc that serves a pitch-shifted 30s preview.
func PreviewShift(
	signer *services.Signer,
	storage services.Storage,
	ytSvc services.YouTubeService,
	shifter services.Shifter,
	coord *services.PreviewCoordinator,
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

		stemShiftedName := "preview-stems/shifted/" + strconv.Itoa(n) + ".wav"
		legacyShiftedName := "preview-shifts/" + strconv.Itoa(n) + ".wav"

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
				// Stem path: shift the clean instrumental stem locally.
				inKey := storage.Key(videoID, "preview-stems/no_vocals.wav")
				outKey := storage.Key(videoID, stemShiftedName)
				doShift := func() error {
					return shiftViaStorage(ctx, storage, shifter, inKey, outKey, ".wav", ".wav", float64(n))
				}
				var shiftErr error
				if coord != nil {
					shiftErr = coord.RunPreviewShift(ctx, videoID, n, func(_ context.Context) error {
						return doShift()
					})
				} else {
					shiftErr = doShift()
				}
				if shiftErr != nil {
					log.Error().Err(shiftErr).Str("videoId", videoID).Int("semitones", n).Msg("stem-shift failed")
					writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
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
			previewHas, err := storage.Has(ctx, storage.Key(videoID, "preview.wav"))
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

			inKey := storage.Key(videoID, "preview.wav")
			outKey := storage.Key(videoID, legacyShiftedName)
			// Use a negated semitone value as the dedup key to distinguish from the
			// stem shift path (they produce different outputs for the same videoID+n).
			legacyKey := -(n + 13)
			doLegacyShift := func() error {
				return shiftViaStorage(ctx, storage, shifter, inKey, outKey, ".wav", ".wav", float64(n))
			}
			var legacyShiftErr error
			if coord != nil {
				legacyShiftErr = coord.RunPreviewShift(ctx, videoID, legacyKey, func(_ context.Context) error {
					return doLegacyShift()
				})
			} else {
				legacyShiftErr = doLegacyShift()
			}
			if legacyShiftErr != nil {
				log.Error().Err(legacyShiftErr).Str("videoId", videoID).Int("semitones", n).Msg("legacy-shift failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
				return
			}
			serveKey = outKey
			serveName = legacyShiftedName
		}

		if url, err := storage.SignGet(ctx, serveKey); err == nil && url != "" {
			http.Redirect(w, r, url, http.StatusFound)
			return
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
