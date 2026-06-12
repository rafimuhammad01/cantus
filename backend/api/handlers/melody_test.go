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
	st, err := services.NewLocalDiskStorage(t.TempDir())
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
// key is "A major" so key-transposition assertions can be added per test case.
var testMelodyPayload = []byte(`{
	"hop_ms": 50,
	"min_hz": 220.0,
	"max_hz": 440.0,
	"key": "A major",
	"frames": [[0, 220.0], [50, 0.0], [100, 440.0], [150, 0.0]]
}`)

// melodyResponse mirrors the shape returned by the Melody handler.
type melodyResponse struct {
	HopMs         int          `json:"hop_ms"`
	MinHz         float64      `json:"min_hz"`
	MaxHz         float64      `json:"max_hz"`
	Key           string       `json:"key"`
	TransposedKey string       `json:"transposed_key"`
	Frames        [][2]float64 `json:"frames"`
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
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q", got.Key, "A major")
				}
				if got.TransposedKey != "A major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "A major")
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
				// A major + 5 = D major (A=9, 9+5=14, 14%12=2, noteNames[2]="D")
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q", got.Key, "A major")
				}
				if got.TransposedKey != "D major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "D major")
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
				// A major - 2 = G major
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q", got.Key, "A major")
				}
				if got.TransposedKey != "G major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "G major")
				}
			},
		},
		{
			name: "happy path semitones=-7 A minor → D minor (Fake Plastic Trees case)",
			url:  "/api/melody/" + validID + "/-7?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				payload := []byte(`{
					"hop_ms": 50,
					"min_hz": 220.0,
					"max_hz": 440.0,
					"key": "A minor",
					"frames": [[0, 220.0], [50, 0.0]]
				}`)
				stageMelody(t, st, validID, payload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "A minor" {
					t.Errorf("key: got %q, want %q", got.Key, "A minor")
				}
				if got.TransposedKey != "D minor" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "D minor")
				}
			},
		},
		{
			name: "happy path C major +5 → F major",
			url:  "/api/melody/" + validID + "/5?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				payload := []byte(`{
					"hop_ms": 50,
					"min_hz": 220.0,
					"max_hz": 440.0,
					"key": "C major",
					"frames": [[0, 220.0], [50, 0.0]]
				}`)
				stageMelody(t, st, validID, payload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "C major" {
					t.Errorf("key: got %q, want %q", got.Key, "C major")
				}
				if got.TransposedKey != "F major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "F major")
				}
			},
		},
		{
			name: "happy path empty key passes through unchanged",
			url:  "/api/melody/" + validID + "/3?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				payload := []byte(`{
					"hop_ms": 50,
					"min_hz": 220.0,
					"max_hz": 440.0,
					"key": "",
					"frames": [[0, 0.0], [50, 0.0]]
				}`)
				stageMelody(t, st, validID, payload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "" {
					t.Errorf("key: got %q, want empty", got.Key)
				}
				if got.TransposedKey != "" {
					t.Errorf("transposed_key: got %q, want empty", got.TransposedKey)
				}
			},
		},
		{
			name: "preview-key.json overrides melody.json key (single source of truth)",
			url:  "/api/melody/" + validID + "/-2?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				// melody.json says "A minor" (Krumhansl on isolated vocals)
				melodyPayload := []byte(`{
					"hop_ms": 50,
					"min_hz": 220.0,
					"max_hz": 440.0,
					"key": "A minor",
					"frames": [[0, 220.0]]
				}`)
				stageMelody(t, st, validID, melodyPayload)
				// preview-key.json says "F major" (chroma on full mix) — must win.
				previewKeyPayload := []byte(`{"key":"F major"}`)
				tmp := filepath.Join(t.TempDir(), "preview-key.json")
				if err := os.WriteFile(tmp, previewKeyPayload, 0o644); err != nil {
					t.Fatalf("write preview-key tmp: %v", err)
				}
				if err := st.Commit(context.Background(), validID, "preview-key.json", tmp); err != nil {
					t.Fatalf("commit preview-key: %v", err)
				}
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "F major" {
					t.Errorf("key: got %q, want %q (preview-key should override)", got.Key, "F major")
				}
				// F major − 2 = D# major (F=5, 5-2=3, noteNames[3]="D#")
				if got.TransposedKey != "D# major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "D# major")
				}
			},
		},
		{
			name: "preview-key.json with empty key falls through to melody.json",
			url:  "/api/melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				melodyPayload := []byte(`{
					"hop_ms": 50,
					"min_hz": 220.0,
					"max_hz": 440.0,
					"key": "A minor",
					"frames": [[0, 220.0]]
				}`)
				stageMelody(t, st, validID, melodyPayload)
				tmp := filepath.Join(t.TempDir(), "preview-key.json")
				if err := os.WriteFile(tmp, []byte(`{"key":""}`), 0o644); err != nil {
					t.Fatalf("write preview-key tmp: %v", err)
				}
				if err := st.Commit(context.Background(), validID, "preview-key.json", tmp); err != nil {
					t.Fatalf("commit preview-key: %v", err)
				}
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "A minor" {
					t.Errorf("key: got %q, want %q (should fall through to melody.json)", got.Key, "A minor")
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
