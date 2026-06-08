package handlers

import (
	"encoding/json"
	"net/http"

	"cantus/backend/services"
)

type generateRequest struct {
	VideoID   string `json:"video_id"`
	Sig       string `json:"sig"`
	Semitones int    `json:"semitones"`
}

// Generate returns an http.HandlerFunc that submits a generate job and returns the jobID.
func Generate(signer *services.Signer, runner services.JobSubmitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req generateRequest
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

		jobID := runner.Submit(req.VideoID, req.Semitones)
		writeJSON(w, http.StatusAccepted, struct {
			JobID string `json:"job_id"`
		}{JobID: jobID})
	}
}
