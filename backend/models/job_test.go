package models_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cantus/backend/models"
)

func TestJobStatus_Constants(t *testing.T) {
	tests := []struct {
		name string
		got  models.JobStatus
		want string
	}{
		{name: "queued", got: models.StatusQueued, want: "queued"},
		{name: "downloading", got: models.StatusDownloading, want: "downloading"},
		{name: "separating", got: models.StatusSeparating, want: "separating"},
		{name: "melody", got: models.StatusMelody, want: "melody"},
		{name: "shifting", got: models.StatusShifting, want: "shifting"},
		{name: "done", got: models.StatusDone, want: "done"},
		{name: "error", got: models.StatusError, want: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := string(tt.got), tt.want; got != want {
				t.Errorf("JobStatus constant: got %q, want %q", got, want)
			}
		})
	}
}

func TestJob_JSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   models.Job
	}{
		{
			name: "all fields populated",
			in: models.Job{
				ID:        "job-abc-123",
				Status:    models.StatusSeparating,
				Message:   "separating vocals",
				CreatedAt: now,
			},
		},
		{
			name: "zero-value job",
			in: models.Job{
				ID:        "job-zero",
				Status:    models.StatusQueued,
				CreatedAt: now,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("json.Marshal error: %v", err)
			}

			var got models.Job
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("json.Unmarshal error: %v", err)
			}

			if got.ID != tt.in.ID {
				t.Errorf("ID: got %q, want %q", got.ID, tt.in.ID)
			}
			if got.Status != tt.in.Status {
				t.Errorf("Status: got %q, want %q", got.Status, tt.in.Status)
			}
			if got.Message != tt.in.Message {
				t.Errorf("Message: got %q, want %q", got.Message, tt.in.Message)
			}
			if !got.CreatedAt.Equal(tt.in.CreatedAt) {
				t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, tt.in.CreatedAt)
			}

			// Verify exact snake_case JSON keys.
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("json.Unmarshal into map error: %v", err)
			}

			expectedKeys := []string{"id", "status", "message", "created_at"}
			for _, key := range expectedKeys {
				if _, ok := m[key]; !ok {
					t.Errorf("JSON key %q missing from encoded output", key)
				}
			}
		})
	}
}

func TestProcessRequest_Validate(t *testing.T) {
	tests := []struct {
		name          string
		req           models.ProcessRequest
		wantErrSubstr string // empty means no error expected
	}{
		// Valid cases.
		{
			name:          "valid videoId and zero semitones",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: 0},
			wantErrSubstr: "",
		},
		{
			name:          "valid semitones at lower bound -5",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: -5},
			wantErrSubstr: "",
		},
		{
			name:          "valid semitones at upper bound +5",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: 5},
			wantErrSubstr: "",
		},

		// Invalid VideoID cases.
		{
			name:          "empty videoId",
			req:           models.ProcessRequest{VideoID: "", Sig: "anysig", Semitones: 0},
			wantErrSubstr: "video_id",
		},
		{
			name:          "videoId too short (10 chars)",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXc", Sig: "anysig", Semitones: 0},
			wantErrSubstr: "video_id",
		},
		{
			name:          "videoId too long (12 chars)",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQQ", Sig: "anysig", Semitones: 0},
			wantErrSubstr: "video_id",
		},
		{
			name:          "videoId contains invalid character !",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgX!Q", Sig: "anysig", Semitones: 0},
			wantErrSubstr: "video_id",
		},

		// Invalid Semitones cases.
		{
			name:          "semitones below -5",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: -6},
			wantErrSubstr: "semitones",
		},
		{
			name:          "semitones above +5",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: 6},
			wantErrSubstr: "semitones",
		},
		{
			name:          "semitones far out of range (100)",
			req:           models.ProcessRequest{VideoID: "dQw4w9WgXcQ", Sig: "anysig", Semitones: 100},
			wantErrSubstr: "semitones",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()

			if tt.wantErrSubstr == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}

			// Error expected.
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err.Error(), tt.wantErrSubstr)
			}
		})
	}
}
