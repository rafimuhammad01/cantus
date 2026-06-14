package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"cantus/backend/logger"
	"cantus/backend/services"
)

type previewStemsRequest struct {
	VideoID string `json:"video_id"`
	Sig     string `json:"sig"`
}

// PreviewStems returns an http.HandlerFunc that runs Demucs + CREPE on the 30s
// preview clip and caches the results. The handler is idempotent: if both
// preview-stems/no_vocals.mp3 and preview-stems/melody.json are already cached it
// returns 200 immediately without touching any upstream service.
func PreviewStems(
	signer *services.Signer,
	storage services.Storage,
	ytSvc services.YouTubeService,
	processor services.ProcessorClient, // used by Stage 4 until Task 8
	gpu services.GPUProcessorClient,
	transcode services.TranscodeFunc,
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

		// Idempotent early-exit: both final outputs already cached → nothing to do.
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

		// Stage 1 — ensure preview.mp3 exists.
		previewHas, err := storage.Has(ctx, storage.Key(videoID, "preview.mp3"))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (preview.mp3) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !previewHas {
			if err := ytSvc.DownloadPreview(ctx, videoID); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("DownloadPreview failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "download failed"})
				return
			}
		}

		// Stage 2 — ensure preview-stems/{vocals,no_vocals}.wav via GPU Separate.
		// Both must be present; if either is missing re-run Demucs so the pair is consistent.
		vocalsHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/vocals.wav"))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (vocals.wav) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		noVocalsWavHas, err := storage.Has(ctx, storage.Key(videoID, "preview-stems/no_vocals.wav"))
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Msg("storage.Has (no_vocals.wav) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}
		if !vocalsHas || !noVocalsWavHas {
			previewKey := storage.Key(videoID, "preview.mp3")
			vocalsKey := storage.Key(videoID, "preview-stems/vocals.wav")
			noVocalsKey := storage.Key(videoID, "preview-stems/no_vocals.wav")

			inURL, err := storage.SignGet(ctx, previewKey)
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignGet (preview.mp3) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign get failed"})
				return
			}
			vocalsPutURL, err := storage.SignPut(ctx, vocalsKey)
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (vocals.wav) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
				return
			}
			noVocalsPutURL, err := storage.SignPut(ctx, noVocalsKey)
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.SignPut (no_vocals.wav) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "sign put failed"})
				return
			}
			if err := gpu.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("processor.Separate failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "separate failed"})
				return
			}
			if err := storage.Verify(ctx, vocalsKey); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (vocals.wav) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "vocals stem not materialized"})
				return
			}
			if err := storage.Verify(ctx, noVocalsKey); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.Verify (no_vocals.wav) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "no_vocals stem not materialized"})
				return
			}
		}

		// Stage 3 — ensure preview-stems/no_vocals.mp3 (ffmpeg transcode of no_vocals.wav).
		if !mp3Has {
			local, ok := storage.(*services.LocalDiskStorage)
			if !ok {
				http.Error(w, "processor unavailable in this storage mode", http.StatusInternalServerError)
				return
			}

			noVocalsWav := local.FilesystemPathForLocalProcessor(local.Key(videoID, "preview-stems/no_vocals.wav"))

			tmpDir, err := os.MkdirTemp("", "cantus-preview-transcode-*")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("os.MkdirTemp failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			tmpMp3 := filepath.Join(tmpDir, "no_vocals.mp3")

			if err := transcode(ctx, noVocalsWav, tmpMp3); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("transcode (no_vocals.mp3) failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "transcode failed"})
				return
			}

			if err := storage.Commit(ctx, storage.Key(videoID, "preview-stems/no_vocals.mp3"), tmpMp3); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("storage.Commit (no_vocals.mp3) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage commit failed"})
				return
			}
		}

		// Stage 4 — ensure preview-stems/melody.json.
		if !melodyHas {
			local, ok := storage.(*services.LocalDiskStorage)
			if !ok {
				http.Error(w, "processor unavailable in this storage mode", http.StatusInternalServerError)
				return
			}

			vocalsPath := local.FilesystemPathForLocalProcessor(local.Key(videoID, "preview-stems/vocals.wav"))
			melodyPath := local.FilesystemPathForLocalProcessor(local.Key(videoID, "preview-stems/melody.json"))
			if err := processor.Melody(ctx, vocalsPath, melodyPath); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Msg("processor.Melody failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "melody failed"})
				return
			}
		}

		writeJSON(w, http.StatusOK, struct {
			Ready bool `json:"ready"`
		}{Ready: true})
	}
}
