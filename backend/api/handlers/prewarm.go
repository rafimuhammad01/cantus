package handlers

import (
	"encoding/json"
	"net/http"

	"cantus/backend/services"
)

type prewarmRequest struct {
	VideoID string `json:"video_id"`
	Sig     string `json:"sig"`
}

// Prewarm returns an http.HandlerFunc that submits a prewarm job (stages 1–3) and returns the jobID.
func Prewarm(signer *services.Signer, runner services.JobSubmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req prewarmRequest
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

		jobID := runner.SubmitPrewarm(req.VideoID)
		writeJSON(w, http.StatusAccepted, struct {
			JobID string `json:"job_id"`
		}{JobID: jobID})
	}
}
