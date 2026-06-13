package handlers_test

import (
	"context"
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

// audioRouter wires a chi router with the Audio handler at /api/audio/{videoId}/{semitones}.
func audioRouter(signer *services.Signer, storage services.Storage) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/audio/{videoId}/{semitones}", handlers.Audio(signer, storage))
	return r
}

// newAudioSigner returns a Signer for tests (32 'x' bytes key).
func newAudioSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// newAudioStorage returns a LocalDiskStorage rooted at a temp dir.
func newAudioStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// mustStage pre-commits content into storage at (videoID, name).
func mustStage(t *testing.T, storage *services.LocalDiskStorage, videoID, name string, content []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "stage")
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := storage.Commit(context.Background(), storage.Key(videoID, name), tmp); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestAudioHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newAudioSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
		url  string

		// setup optionally pre-stages files into storage before the request.
		setup func(t *testing.T, st *services.LocalDiskStorage)

		wantStatus       int
		wantBody         string
		wantBodyContains string
	}{
		{
			name: "happy path cache hit semitones=-2",
			url:  "/api/audio/" + validID + "/-2?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				mustStage(t, st, validID, "shifted/-2/audio.mp3", []byte("fake mp3 bytes"))
			},
			wantStatus: http.StatusOK,
			wantBody:   "fake mp3 bytes",
		},
		{
			name: "happy path semitones=0",
			url:  "/api/audio/" + validID + "/0?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				mustStage(t, st, validID, "shifted/0/audio.mp3", []byte("zero semitones"))
			},
			wantStatus: http.StatusOK,
			wantBody:   "zero semitones",
		},
		{
			name: "happy path semitones=+5",
			url:  "/api/audio/" + validID + "/5?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				mustStage(t, st, validID, "shifted/5/audio.mp3", []byte("plus five"))
			},
			wantStatus: http.StatusOK,
			wantBody:   "plus five",
		},
		{
			name:             "cache miss returns 404",
			url:              "/api/audio/" + validID + "/-2?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusNotFound,
			wantBodyContains: "error",
		},
		{
			name:             "invalid videoID",
			url:              "/api/audio/short/0?sig=anything",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name:             "invalid sig",
			url:              "/api/audio/" + validID + "/-2?sig=deadbeef",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "missing sig",
			url:              "/api/audio/" + validID + "/-2",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "semitones=13 out of range",
			url:              "/api/audio/" + validID + "/13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "semitones=-13 out of range",
			url:              "/api/audio/" + validID + "/-13?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
		{
			name:             "semitones=abc non-numeric",
			url:              "/api/audio/" + validID + "/abc?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "semitones",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newAudioStorage(t)
			tt.setup(t, st)
			router := audioRouter(signer, st)

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

			if tt.wantBody != "" {
				if got := rec.Body.String(); got != tt.wantBody {
					t.Errorf("body: got %q, want %q", got, tt.wantBody)
				}
			}
		})
	}
}

func TestAudioHandler_RangeRequest(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newAudioSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
	}{
		{name: "range request returns 206 partial content"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newAudioStorage(t)
			content := make([]byte, 100)
			for i := range content {
				content[i] = byte(i)
			}
			mustStage(t, st, validID, "shifted/0/audio.mp3", content)

			router := audioRouter(signer, st)

			req := httptest.NewRequest(http.MethodGet, "/api/audio/"+validID+"/0?sig="+validSig, nil)
			req.Header.Set("Range", "bytes=0-9")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got := rec.Code; got != http.StatusPartialContent {
				t.Errorf("status: got %d, want %d (body: %s)", got, http.StatusPartialContent, rec.Body.String())
			}
			if got := len(rec.Body.Bytes()); got != 10 {
				t.Errorf("body length: got %d, want 10", got)
			}
		})
	}
}
