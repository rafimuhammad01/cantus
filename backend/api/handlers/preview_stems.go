package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"

	"cantus/backend/logger"
	"cantus/backend/services"
)

// writeToFile copies data from rc into the file at path (creating it if needed).
func writeToFile(path string, rc io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, rc)
	return err
}

type previewStemsRequest struct {
	VideoID string `json:"video_id"`
	Sig     string `json:"sig"`
}

// runPreviewStemsHelper runs stages 1-4 of the preview-stems pipeline.
// Returns ("", nil) on success; (userMsg, err) on failure where userMsg is safe to send to the client.
func runPreviewStemsHelper(
	ctx context.Context,
	log zerolog.Logger,
	videoID string,
	storage services.Storage,
	ytSvc services.YouTubeService,
	processor services.ProcessorClient,
	transcode services.TranscodeFunc,
	failures *services.VideoFailureTracker,
	mp3Has bool,
	melodyHas bool,
) (string, error) {
	// Stage 1 — ensure preview.mp3 exists.
	previewHas, err := storage.Has(ctx, storage.Key(videoID, "preview.mp3"))
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

	// Stage 2 — ensure preview-stems/{vocals,no_vocals}.wav via GPU Separate.
	// Both must be present; if either is missing re-run Demucs so the pair is consistent.
	vocalsHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/vocals.wav"))
	if err != nil {
		log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (vocals.wav) failed")
		return "storage check failed", err
	}
	noVocalsWavHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals.wav"))
	if err != nil {
		log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (no_vocals.wav) failed")
		return "storage check failed", err
	}
	if !vocalsHas || !noVocalsWavHas {
		previewKey := storage.Key(videoID, "preview.mp3")
		vocalsKey := storage.Key(videoID, "preview-stems/vocals.wav")
		noVocalsKey := storage.Key(videoID, "preview-stems/no_vocals.wav")

		inURL, err := storage.SignGet(ctx, previewKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignGet (preview.mp3) failed")
			return "sign get failed", err
		}
		vocalsPutURL, err := storage.SignPut(ctx, vocalsKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (vocals.wav) failed")
			return "sign put failed", err
		}
		noVocalsPutURL, err := storage.SignPut(ctx, noVocalsKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (no_vocals.wav) failed")
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
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (vocals.wav) failed")
			failures.RecordFailure(videoID)
			return "vocals stem not materialized", err
		}
		if err := storage.Verify(ctx, noVocalsKey); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (no_vocals.wav) failed")
			failures.RecordFailure(videoID)
			return "no_vocals stem not materialized", err
		}
	}

	// Stage 3 — ensure preview-stems/no_vocals.mp3 (ffmpeg transcode of no_vocals.wav).
	if !mp3Has {
		tmpDir, err := os.MkdirTemp("", "cantus-preview-transcode-*")
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("os.MkdirTemp failed")
			return "storage path failed", err
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()

		// Copy the WAV from storage to a local temp file so ffmpeg can read it.
		noVocalsWavKey := storage.Key(videoID, "preview-stems/no_vocals.wav")
		rc, err := storage.Open(ctx, noVocalsWavKey)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Open (no_vocals.wav) failed")
			return "storage open failed", err
		}
		tmpWav := filepath.Join(tmpDir, "no_vocals.wav")
		if err := writeToFile(tmpWav, rc); err != nil {
			_ = rc.Close()
			log.Error().Err(err).Str("videoId", videoID).Msg("copy no_vocals.wav to temp failed")
			return "storage read failed", err
		}
		_ = rc.Close()

		tmpMp3 := filepath.Join(tmpDir, "no_vocals.mp3")

		if err := transcode(ctx, tmpWav, tmpMp3); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("transcode (no_vocals.mp3) failed")
			return "transcode failed", err
		}

		if err := storage.Commit(ctx, storage.Key(videoID, "preview-stems/no_vocals.mp3"), tmpMp3); err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Commit (no_vocals.mp3) failed")
			return "storage commit failed", err
		}
	}

	// Stage 4 — ensure preview-stems/melody.json.
	if !melodyHas {
		vocalsKey := storage.Key(videoID, "preview-stems/vocals.wav")
		melodyKey := storage.Key(videoID, "preview-stems/melody.json")
		vocalsURL, err := storage.SignGet(ctx, vocalsKey)
		if err != nil {
			log.Error().Err(err).Msg("storage.SignGet (vocals.wav) failed")
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
	transcode services.TranscodeFunc,
	failures *services.VideoFailureTracker,
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
		mp3Has, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals.mp3"))
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
		if mp3Has && melodyHas {
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
			msg, err := runPreviewStemsHelper(ctx, log, videoID, storage, ytSvc, processor, transcode, failures, mp3Has, melodyHas)
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
