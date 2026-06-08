package models

import (
	"fmt"
	"regexp"
	"time"
)

type JobStatus string

const (
	StatusQueued      JobStatus = "queued"
	StatusDownloading JobStatus = "downloading"
	StatusSeparating  JobStatus = "separating"
	StatusMelody      JobStatus = "melody"
	StatusShifting    JobStatus = "shifting"
	StatusProcessing  JobStatus = "processing" // retained for backward compat; not used by JobRunner
	StatusDone        JobStatus = "done"
	StatusError       JobStatus = "error"
)

type Job struct {
	ID         string    `json:"id"`
	Status     JobStatus `json:"status"`
	Message    string    `json:"message"`
	Progress   int       `json:"progress"`
	OutputPath string    `json:"output_path"`
	CreatedAt  time.Time `json:"created_at"`
}

type ProcessRequest struct {
	VideoID   string `json:"video_id"`
	Sig       string `json:"sig"`
	Semitones int    `json:"semitones"`
}

var videoIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func (r ProcessRequest) Validate() error {
	if !videoIDRegex.MatchString(r.VideoID) {
		return fmt.Errorf("video_id %q is invalid: must be 11 alphanumeric/dash/underscore characters", r.VideoID)
	}
	if r.Semitones < -5 || r.Semitones > 5 {
		return fmt.Errorf("semitones %d out of range [-5, +5]", r.Semitones)
	}
	return nil
}
