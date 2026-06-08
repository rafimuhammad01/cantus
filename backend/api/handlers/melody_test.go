package handlers_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/services"
)

// melodyRouter wires a chi router with the Melody handler.
func melodyRouter(signer *services.Signer, storage services.Storage) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/melody/{videoId}/{semitones}", handlers.Melody(signer, storage))
	return r
}

// newMelodySigner returns a Signer for tests (32 'x' bytes key).
func newMelodySigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// newMelodyStorage returns a LocalDiskStorage rooted at a temp dir.
func newMelodyStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir(), 1*time.Hour)
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// stageMelody pre-stages a melody.json payload into storage for the given videoID.
func stageMelody(t *testing.T, storage *services.LocalDiskStorage, videoID string, payload []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "melody.json")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := storage.Commit(context.Background(), videoID, "melody.json", tmp); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// testMelodyPayload is the canonical small fixture for melody tests.
var testMelodyPayload = []byte(`{
	"hop_ms": 50,
	"min_hz": 220.0,
	"max_hz": 440.0,
	"frames": [[0, 220.0], [50, 0.0], [100, 440.0], [150, 0.0]]
}`)

// melodyResponse mirrors the shape returned by the Melody handler.
type melodyResponse struct {
	HopMs  int          `json:"hop_ms"`
	MinHz  float64      `json:"min_hz"`
	MaxHz  float64      `json:"max_hz"`
	Frames [][2]float64 `json:"frames"`
}

func TestMelodyHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newMelodySigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name  string
		url   string
		setup func(t *testing.T, st *services.LocalDiskStorage)

		wantStatus       int
		wantBodyContains string

		// checkBody is called (when non-nil) to validate the decoded response.
		checkBody func(t *testing.T, got melodyResponse)
	}{
		{
			name: "happy path semitones=0 values unchanged",
			url:  "/api/melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stageMelody(t, st, validID, testMelodyPayload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.HopMs != 50 {
					t.Errorf("hop_ms: got %d, want 50", got.HopMs)
				}
				if math.Abs(got.MinHz-220.0) > 0.01 {
					t.Errorf("min_hz: got %f, want ~220.0", got.MinHz)
				}
				if math.Abs(got.MaxHz-440.0) > 0.01 {
					t.Errorf("max_hz: got %f, want ~440.0", got.MaxHz)
				}
				if math.Abs(got.Frames[0][1]-220.0) > 0.01 {
					t.Errorf("frames[0][1]: got %f, want ~220.0", got.Frames[0][1])
				}
				if got.Frames[1][1] != 0.0 {
					t.Errorf("frames[1][1] (unvoiced): got %f, want exactly 0.0", got.Frames[1][1])
				}
				if math.Abs(got.Frames[2][1]-440.0) > 0.01 {
					t.Errorf("frames[2][1]: got %f, want ~440.0", got.Frames[2][1])
				}
				if got.Frames[3][1] != 0.0 {
					t.Errorf("frames[3][1] (unvoiced): got %f, want exactly 0.0", got.Frames[3][1])
				}
			},
		},
		{
			name: "happy path semitones=+5 scales hz correctly",
			url:  "/api/melody/" + validID + "/5?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stageMelody(t, st, validID, testMelodyPayload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				ratio := math.Pow(2, 5.0/12)
				if math.Abs(got.MinHz-220.0*ratio) > 0.01 {
					t.Errorf("min_hz: got %f, want ~%f", got.MinHz, 220.0*ratio)
				}
				if math.Abs(got.MaxHz-440.0*ratio) > 0.01 {
					t.Errorf("max_hz: got %f, want ~%f", got.MaxHz, 440.0*ratio)
				}
				if math.Abs(got.Frames[0][1]-220.0*ratio) > 0.01 {
					t.Errorf("frames[0][1]: got %f, want ~%f", got.Frames[0][1], 220.0*ratio)
				}
				if got.Frames[1][1] != 0.0 {
					t.Errorf("frames[1][1] (unvoiced): got %f, want exactly 0.0", got.Frames[1][1])
				}
				if math.Abs(got.Frames[2][1]-440.0*ratio) > 0.01 {
					t.Errorf("frames[2][1]: got %f, want ~%f", got.Frames[2][1], 440.0*ratio)
				}
				if got.Frames[3][1] != 0.0 {
					t.Errorf("frames[3][1] (unvoiced): got %f, want exactly 0.0", got.Frames[3][1])
				}
			},
		},
		{
			name: "happy path semitones=-2 scales hz correctly",
			url:  "/api/melody/" + validID + "/-2?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stageMelody(t, st, validID, testMelodyPayload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				ratio := math.Pow(2, -2.0/12)
				if math.Abs(got.MinHz-220.0*ratio) > 0.01 {
					t.Errorf("min_hz: got %f, want ~%f", got.MinHz, 220.0*ratio)
				}
				if math.Abs(got.MaxHz-440.0*ratio) > 0.01 {
					t.Errorf("max_hz: got %f, want ~%f", got.MaxHz, 440.0*ratio)
				}
				if math.Abs(got.Frames[0][1]-220.0*ratio) > 0.01 {
					t.Errorf("frames[0][1]: got %f, want ~%f", got.Frames[0][1], 220.0*ratio)
				}
				if got.Frames[1][1] != 0.0 {
					t.Errorf("frames[1][1] (unvoiced): got %f, want exactly 0.0", got.Frames[1][1])
				}
				if math.Abs(got.Frames[2][1]-440.0*ratio) > 0.01 {
					t.Errorf("frames[2][1]: got %f, want ~%f", got.Frames[2][1], 440.0*ratio)
				}
				if got.Frames[3][1] != 0.0 {
					t.Errorf("frames[3][1] (unvoiced): got %f, want exactly 0.0", got.Frames[3][1])
				}
			},
		},
		{
			name:             "semitones=13 out of range",
			url:              "/api/melody/" + validID + "/13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "semitones=-13 out of range",
			url:              "/api/melody/" + validID + "/-13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "semitones=abc non-numeric",
			url:              "/api/melody/" + validID + "/abc?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "invalid videoID",
			url:              "/api/melody/short/0?sig=anything",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name:             "invalid sig",
			url:              "/api/melody/" + validID + "/0?sig=deadbeef",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "missing sig",
			url:              "/api/melody/" + validID + "/0",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "cache miss no melody.json staged",
			url:              "/api/melody/" + validID + "/0?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusNotFound,
			wantBodyContains: "melody not generated",
		},
		{
			name: "corrupted melody.json returns 500",
			url:  "/api/melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stageMelody(t, st, validID, []byte("this is not json {{{"))
			},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: "melody parse failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMelodyStorage(t)
			tt.setup(t, st)
			router := melodyRouter(signer, st)

			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
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

			if tt.checkBody != nil {
				if rec.Code == http.StatusOK {
					ct := rec.Header().Get("Content-Type")
					if !strings.Contains(ct, "application/json") {
						t.Errorf("Content-Type: got %q, want it to contain application/json", ct)
					}
					var got melodyResponse
					if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
						t.Fatalf("decode response body: %v", err)
					}
					tt.checkBody(t, got)
				}
			}
		})
	}
}
