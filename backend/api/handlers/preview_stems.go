package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"cantus/backend/logger"
	"cantus/backend/services"
)

type previewStemsRequest struct {
	VideoID string `json:"video_id"`
	Sig     string `json:"sig"`
}

// runPreviewStemsHelper runs stages 1-3 of the preview-stems pipeline.
// Returns ("", nil) on success; (userMsg, err) on failure where userMsg is safe to send to the client.
func runPreviewStemsHelper(
	ctx context.Context,
	log zerolog.Logger,
	videoID string,
	storage services.Storage,
	ytSvc services.YouTubeService,
	processor services.ProcessorClient,
	failures *services.VideoFailureTracker,
	noVocalsWavHas bool,
	melodyHas bool,
) (string, error) {
	// Stage 1 — ensure preview.mp3 exists.
	previewHas, err := storage.Has(ctx, storage.Key(videoID, "preview"+services.AudioExt))
	if err != nil {
		log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (preview.mp3) failed")
		return "storage check failed", err
	}
	if !previewHas {
		if err := services.Retry(ctx, services.PipelineRetryAttempts, services.PipelineRetryBaseDelay, func() error {
			return ytSvc.DownloadPreview(ctx, videoID)
		}); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("DownloadPreview failed")
			failures.RecordFailure(videoID)
			return "download failed", err
		}
	}

	// Stage 2 — ensure preview-stems/{vocals,no_vocals}.mp3 via GPU Separate.
	// Both must be present; if either is missing re-run Demucs so the pair is consistent.
	vocalsHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/vocals"+services.AudioExt))
	if err != nil {
		log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (vocals.mp3) failed")
		return "storage check failed", err
	}
	noVocalsWavHasNow, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals"+services.AudioExt))
	if err != nil {
		log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (no_vocals.mp3) failed")
		return "storage check failed", err
	}
	if !vocalsHas || !noVocalsWavHasNow {
		previewKey := storage.Key(videoID, "preview"+services.AudioExt)
		vocalsKey := storage.Key(videoID, "preview-stems/vocals"+services.AudioExt)
		noVocalsKey := storage.Key(videoID, "preview-stems/no_vocals"+services.AudioExt)

		inURL, err := storage.SignGet(ctx, previewKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignGet (preview.mp3) failed")
			return "sign get failed", err
		}
		vocalsPutURL, err := storage.SignPut(ctx, vocalsKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (vocals.mp3) failed")
			return "sign put failed", err
		}
		noVocalsPutURL, err := storage.SignPut(ctx, noVocalsKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (no_vocals.mp3) failed")
			return "sign put failed", err
		}
		if err := services.Retry(ctx, services.PipelineRetryAttempts, services.PipelineRetryBaseDelay, func() error {
			return processor.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL)
		}); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("processor.Separate failed")
			failures.RecordFailure(videoID)
			return "separate failed", err
		}
		if err := storage.Verify(ctx, vocalsKey); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (vocals.mp3) failed")
			failures.RecordFailure(videoID)
			return "vocals stem not materialized", err
		}
		if err := storage.Verify(ctx, noVocalsKey); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (no_vocals.mp3) failed")
			failures.RecordFailure(videoID)
			return "no_vocals stem not materialized", err
		}
	}

	// Stage 3 — ensure preview-stems/melody.json.
	if !melodyHas {
		vocalsKey := storage.Key(videoID, "preview-stems/vocals"+services.AudioExt)
		melodyKey := storage.Key(videoID, "preview-stems/melody.json")
		vocalsURL, err := storage.SignGet(ctx, vocalsKey)
		if err != nil {
			log.Error().Err(err).Msg("storage.SignGet (vocals.mp3) failed")
			return "sign get failed", err
		}
		outURL, err := storage.SignPut(ctx, melodyKey)
		if err != nil {
			log.Error().Err(err).Msg("storage.SignPut (melody.json) failed")
			return "sign put failed", err
		}
		if err := services.Retry(ctx, services.PipelineRetryAttempts, services.PipelineRetryBaseDelay, func() error {
			return processor.Melody(ctx, vocalsURL, outURL)
		}); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("processor.Melody failed")
			failures.RecordFailure(videoID)
			return "melody failed", err
		}
		if err := storage.Verify(ctx, melodyKey); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (melody.json) failed")
			failures.RecordFailure(videoID)
			return "melody not materialized", err
		}
	}

	failures.RecordSuccess(videoID)
	return "", nil
}

// PreviewStems returns an http.HandlerFunc that runs Demucs + CREPE on the 30s
// preview clip and caches the results. The handler is idempotent: if both
// preview-stems/no_vocals.mp3 and preview-stems/melody.json are already cached it
// returns 200 immediately without touching any upstream service.
func PreviewStems(
	signer *services.Signer,
	storage services.Storage,
	ytSvc services.YouTubeService,
	processor services.ProcessorClient,
	failures *services.VideoFailureTracker,
	coord *services.PreviewCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req previewStemsRequest
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

		ctx := r.Context()
		log := logger.FromCtx(ctx)
		videoID := req.VideoID

		// Short-circuit blocked videoIDs before doing any GPU work.
		if failures.IsBlocked(videoID) {
			log.Warn().Str("videoId", videoID).Msg("preview-stems blocked: video has exceeded failure cap")
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "video temporarily unavailable after repeated failures, try again later"})
			return
		}

		// Phase 2 — idempotent fast-path: both final outputs already cached.
		noVocalsWavHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals"+services.AudioExt))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (no_vocals.mp3) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		melodyHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/melody.json"))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (melody.json) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if noVocalsWavHas && melodyHas {
			writeJSON(w, http.StatusOK, struct {
				Ready bool `json:"ready"`
			}{Ready: true})
			return
		}

		// Phase 3 — streaming pipeline. Start 200 response now so the proxy does
		// not time out during long-running Modal calls (~23s + ~24s).
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}

		type result struct {
			msg string
			err error
		}
		done := make(chan result, 1)
		go func() {
			var msg string
			var err error
			if coord != nil {
				err = coord.RunPreviewStems(ctx, videoID, func(innerCtx context.Context) error {
					msg, err = runPreviewStemsHelper(innerCtx, log, videoID, storage, ytSvc, processor, failures, noVocalsWavHas, melodyHas)
					return err
				})
				if err != nil && msg == "" {
					msg = err.Error()
				}
			} else {
				msg, err = runPreviewStemsHelper(ctx, log, videoID, storage, ytSvc, processor, failures, noVocalsWavHas, melodyHas)
			}
			done <- result{msg, err}
		}()

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case res := <-done:
				if res.err != nil {
					_ = json.NewEncoder(w).Encode(map[string]string{"error": res.msg})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]bool{"ready": true})
				}
				if flusher != nil {
					flusher.Flush()
				}
				return
			case <-ticker.C:
				_, _ = w.Write([]byte(" "))
				if flusher != nil {
					flusher.Flush()
				}
			case <-ctx.Done():
				return
			}
		}
	}
}
