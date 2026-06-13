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

// previewMelodyRouter wires a chi router with the PreviewMelody handler.
func previewMelodyRouter(signer *services.Signer, storage services.Storage) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/preview-melody/{videoId}/{semitones}", handlers.PreviewMelody(signer, storage))
	return r
}

// newPreviewMelodySigner returns a Signer for tests (32 'm' bytes key).
func newPreviewMelodySigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("m", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// newPreviewMelodyStorage returns a LocalDiskStorage rooted at a temp dir.
func newPreviewMelodyStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// stagePreviewMelodyJSON pre-stages a preview-stems/melody.json into storage.
func stagePreviewMelodyJSON(t *testing.T, storage *services.LocalDiskStorage, videoID string, payload []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "melody.json")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := storage.Commit(context.Background(), storage.Key(videoID, "preview-stems/melody.json"), tmp); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// stagePreviewKeyJSON pre-stages a preview-key.json into storage for a videoID.
func stagePreviewKeyJSON(t *testing.T, storage *services.LocalDiskStorage, videoID, key string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "preview-key.json")
	payload := []byte(`{"key":"` + key + `"}`)
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatalf("write preview-key tmp: %v", err)
	}
	if err := storage.Commit(context.Background(), storage.Key(videoID, "preview-key.json"), tmp); err != nil {
		t.Fatalf("commit preview-key: %v", err)
	}
}

// testPreviewMelodyPayload is the canonical fixture. key="A major" lets us assert
// key transposition clearly.
var testPreviewMelodyPayload = []byte(`{
	"hop_ms": 50,
	"min_hz": 220.0,
	"max_hz": 440.0,
	"key": "A major",
	"frames": [[0, 220.0], [50, 0.0], [100, 440.0], [150, 0.0]]
}`)

func TestPreviewMelodyHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewMelodySigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name  string
		url   string
		setup func(t *testing.T, st *services.LocalDiskStorage)

		wantStatus       int
		wantBodyContains string

		// checkBody is called (when non-nil) only on 200 responses.
		checkBody func(t *testing.T, got melodyResponse)
	}{
		{
			name: "200 happy path semitones=0 — key and frames unchanged",
			url:  "/api/preview-melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stagePreviewMelodyJSON(t, st, validID, testPreviewMelodyPayload)
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
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q", got.Key, "A major")
				}
				if got.TransposedKey != "A major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "A major")
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
			name: "200 semitones=+7 — hz scaled by 2^(7/12), transposed_key advances 7, unvoiced stays 0",
			url:  "/api/preview-melody/" + validID + "/7?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stagePreviewMelodyJSON(t, st, validID, testPreviewMelodyPayload)
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				ratio := math.Pow(2, 7.0/12)
				if math.Abs(got.MinHz-220.0*ratio) > 0.01 {
					t.Errorf("min_hz: got %f, want ~%f", got.MinHz, 220.0*ratio)
				}
				if math.Abs(got.MaxHz-440.0*ratio) > 0.01 {
					t.Errorf("max_hz: got %f, want ~%f", got.MaxHz, 440.0*ratio)
				}
				// A major + 7 = E major (A=9, 9+7=16, 16%12=4, noteNames[4]="E")
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q", got.Key, "A major")
				}
				if got.TransposedKey != "E major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "E major")
				}
				if math.Abs(got.Frames[0][1]-220.0*ratio) > 0.01 {
					t.Errorf("frames[0][1]: got %f, want ~%f", got.Frames[0][1], 220.0*ratio)
				}
				// unvoiced frame must stay exactly 0
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
			name: "200 preview-key.json present does NOT override melody.json key",
			url:  "/api/preview-melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				// melody.json says "A major" (testPreviewMelodyPayload); preview-key.json
				// disagrees with "F major". melody.json must win — chroma-on-full-mix is
				// prone to relative-minor / IV-of confusion and is unsafe as an override.
				stagePreviewMelodyJSON(t, st, validID, testPreviewMelodyPayload)
				stagePreviewKeyJSON(t, st, validID, "F major")
			},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, got melodyResponse) {
				t.Helper()
				if got.Key != "A major" {
					t.Errorf("key: got %q, want %q (melody.json must win)", got.Key, "A major")
				}
				if got.TransposedKey != "A major" {
					t.Errorf("transposed_key: got %q, want %q", got.TransposedKey, "A major")
				}
			},
		},
		{
			name:             "404 cache miss — preview-stems not generated",
			url:              "/api/preview-melody/" + validID + "/0?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusNotFound,
			wantBodyContains: "preview melody not generated",
		},
		{
			name:             "400 invalid videoID",
			url:              "/api/preview-melody/short/0?sig=anything",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name:             "400 semitones non-numeric",
			url:              "/api/preview-melody/" + validID + "/abc?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "400 semitones=13 out of range",
			url:              "/api/preview-melody/" + validID + "/13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "400 semitones=-13 out of range",
			url:              "/api/preview-melody/" + validID + "/-13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "400 missing sig",
			url:              "/api/preview-melody/" + validID + "/0",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "400 invalid sig",
			url:              "/api/preview-melody/" + validID + "/0?sig=deadbeef",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name: "500 JSON parse error — corrupted melody.json",
			url:  "/api/preview-melody/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stagePreviewMelodyJSON(t, st, validID, []byte("not json {{{"))
			},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: "melody parse failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newPreviewMelodyStorage(t)
			tt.setup(t, st)
			router := previewMelodyRouter(signer, st)

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

			if tt.checkBody != nil && rec.Code == http.StatusOK {
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
		})
	}
}

// TestPreviewMelodyHandler_StorageError tests that storage.Has failure returns 500.
func TestPreviewMelodyHandler_StorageError(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewMelodySigner(t)
	validSig := signer.Sign(validID)

	real := newPreviewMelodyStorage(t)
	st := &errStorage{Storage: real, errOnHasName: "preview-stems/melody.json"}
	router := previewMelodyRouter(signer, st)

	req := httptest.NewRequest(http.MethodGet, "/api/preview-melody/"+validID+"/0?sig="+validSig, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d (body: %s)", got, http.StatusInternalServerError, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "storage check failed") {
		t.Errorf("body: got %q, want it to contain %q", body, "storage check failed")
	}
}

// TestPreviewMelodyHandler_TTLExpiry confirms that the handler reads from
// preview-stems/melody.json (not the full-pipeline melody.json).
func TestPreviewMelodyHandler_CorrectPath(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewMelodySigner(t)
	validSig := signer.Sign(validID)

	st := newPreviewMelodyStorage(t)

	// Only stage the preview-stems path; full-pipeline melody.json is absent.
	stagePreviewMelodyJSON(t, st, validID, []byte(`{
		"hop_ms": 25,
		"min_hz": 100.0,
		"max_hz": 200.0,
		"key": "C major",
		"frames": [[0, 100.0]]
	}`))

	router := previewMelodyRouter(signer, st)

	req := httptest.NewRequest(http.MethodGet, "/api/preview-melody/"+validID+"/0?sig="+validSig, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var got melodyResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HopMs != 25 {
		t.Errorf("hop_ms: got %d, want 25 — wrong file served", got.HopMs)
	}
}
