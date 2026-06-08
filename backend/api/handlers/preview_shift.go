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
		name := "preview-shifts/" + strconv.Itoa(req.Semitones) + ".mp3"

		ok, err := storage.Has(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("storage.Has (shifted) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
			return
		}

		if !ok {
			previewHas, err := storage.Has(ctx, videoID, "preview.mp3")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("storage.Has (preview) failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage check failed"})
				return
			}

			if !previewHas {
				if err := ytSvc.DownloadPreview(ctx, videoID); err != nil {
					log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("DownloadPreview failed")
					writeJSON(w, http.StatusBadGateway, errorResponse{Error: "download failed"})
					return
				}
			}

			inputPath, err := storage.LocalPath(ctx, videoID, "preview.mp3")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("storage.LocalPath failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}

			tmpDir, err := os.MkdirTemp("", "cantus-shift-*")
			if err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("os.MkdirTemp failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
				return
			}
			defer func() { _ = os.RemoveAll(tmpDir) }()

			outputPath := filepath.Join(tmpDir, "shifted.mp3")

			if err := processor.Shift(ctx, inputPath, outputPath, float64(req.Semitones)); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("processor.Shift failed")
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "shift failed"})
				return
			}

			if err := storage.Commit(ctx, videoID, name, outputPath); err != nil {
				log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("storage.Commit failed")
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage commit failed"})
				return
			}
		}

		path, err := storage.LocalPath(ctx, videoID, name)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("storage.LocalPath (serve) failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		f, err := os.Open(path)
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("os.Open failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			log.Error().Err(err).Str("videoId", videoID).Int("semitones", req.Semitones).Msg("file.Stat failed")
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "storage path failed"})
			return
		}

		http.ServeContent(w, r, "preview-shifts/"+strconv.Itoa(req.Semitones)+".mp3", info.ModTime(), f)
	}
}
