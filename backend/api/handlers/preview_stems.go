package handlers

import (
	"encoding/json"
	"net/http"

	"cantus/backend/services"
)

type previewStemsRequest struct {
	VideoID string `json:"video_id"`
	Sig     string `json:"sig"`
}

// PreviewStems returns an http.HandlerFunc that enqueues Demucs + CREPE on the
// 30s preview clip. It responds immediately with {job_id}; the client polls
// /api/status/:jobId via SSE for per-stage progress.
func PreviewStems(signer *services.Signer, submitter services.JobSubmitter) http.HandlerFunc {
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

		jobID := submitter.SubmitPreviewStems(req.VideoID)
		writeJSON(w, http.StatusOK, struct {
			JobID string `json:"job_id"`
		}{JobID: jobID})
	}
}
