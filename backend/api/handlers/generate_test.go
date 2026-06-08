package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/services"
)

// fakeRunner is a test double for services.JobSubmitter.
type fakeRunner struct {
	submitted []struct {
		videoID   string
		semitones int
	}
	returnID string
}

func (f *fakeRunner) Submit(videoID string, semitones int) string {
	f.submitted = append(f.submitted, struct {
		videoID   string
		semitones int
	}{videoID, semitones})
	if f.returnID == "" {
		return "fake-job-id-deadbeef"
	}
	return f.returnID
}

// generateRouter wires a chi router with the Generate handler at POST /api/generate.
func generateRouter(signer *services.Signer, runner services.JobSubmitter) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/generate", handlers.Generate(signer, runner))
	return r
}

// newGenerateSigner returns a Signer for tests (32 'g' bytes key).
func newGenerateSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("g", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

func TestGenerateHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newGenerateSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name             string
		body             string
		wantStatus       int
		wantBodyContains string
		wantJobID        bool // assert job_id field present in response
		wantSubmitCount  int
		wantVideoID      string
		wantSemitones    int
	}{
		{
			name:            "happy path",
			body:            `{"video_id":"` + validID + `","sig":"` + validSig + `","semitones":-2}`,
			wantStatus:      http.StatusAccepted,
			wantJobID:       true,
			wantSubmitCount: 1,
			wantVideoID:     validID,
			wantSemitones:   -2,
		},
		{
			name:             "malformed JSON body",
			body:             "not json",
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid request body",
			wantSubmitCount:  0,
		},
		{
			name:             "invalid videoID — 10 chars",
			body:             `{"video_id":"short12345","sig":"anything","semitones":0}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
			wantSubmitCount:  0,
		},
		{
			name:             "invalid sig",
			body:             `{"video_id":"` + validID + `","sig":"deadbeef","semitones":0}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
			wantSubmitCount:  0,
		},
		{
			name:             "missing sig field",
			body:             `{"video_id":"` + validID + `","semitones":0}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
			wantSubmitCount:  0,
		},
		{
			name:             "semitones=-13 out of range",
			body:             `{"video_id":"` + validID + `","sig":"` + validSig + `","semitones":-13}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones must be in [-12, 12]",
			wantSubmitCount:  0,
		},
		{
			name:             "semitones=13 out of range",
			body:             `{"video_id":"` + validID + `","sig":"` + validSig + `","semitones":13}`,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones must be in [-12, 12]",
			wantSubmitCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			router := generateRouter(signer, runner)

			req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got, want := rec.Code, tt.wantStatus; got != want {
				t.Errorf("status: got %d, want %d (body: %s)", got, want, rec.Body.String())
			}

			if tt.wantBodyContains != "" {
				body := rec.Body.String()
				if !strings.Contains(body, tt.wantBodyContains) {
					t.Errorf("body: got %q, want it to contain %q", body, tt.wantBodyContains)
				}
			}

			if tt.wantJobID {
				var resp struct {
					JobID string `json:"job_id"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.JobID == "" {
					t.Errorf("job_id: got empty string, want non-empty")
				}
			}

			if got, want := len(runner.submitted), tt.wantSubmitCount; got != want {
				t.Errorf("Submit call count: got %d, want %d", got, want)
			}

			if tt.wantSubmitCount > 0 && len(runner.submitted) > 0 {
				if got := runner.submitted[0].videoID; got != tt.wantVideoID {
					t.Errorf("Submit videoID: got %q, want %q", got, tt.wantVideoID)
				}
				if got := runner.submitted[0].semitones; got != tt.wantSemitones {
					t.Errorf("Submit semitones: got %d, want %d", got, tt.wantSemitones)
				}
			}
		})
	}
}
