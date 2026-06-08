package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/models"
	"cantus/backend/services"
)

// Status returns an http.HandlerFunc that streams SSE status updates for a job.
func Status(jobStore *services.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := chi.URLParam(r, "jobId")

		job, ok := jobStore.Get(jobID)
		if !ok {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "job not found"})
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "streaming not supported"})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		sendEvent(w, job)
		flusher.Flush()

		if job.Status == models.StatusDone || job.Status == models.StatusError {
			return
		}

		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		lastStatus := job.Status

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				current, ok := jobStore.Get(jobID)
				if !ok {
					return
				}
				if current.Status != lastStatus {
					sendEvent(w, current)
					flusher.Flush()
					lastStatus = current.Status
				}
				if current.Status == models.StatusDone || current.Status == models.StatusError {
					return
				}
			}
		}
	}
}

func sendEvent(w io.Writer, job models.Job) {
	payload, _ := json.Marshal(struct {
		Status  models.JobStatus `json:"status"`
		Message string           `json:"message"`
	}{Status: job.Status, Message: job.Message})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}
