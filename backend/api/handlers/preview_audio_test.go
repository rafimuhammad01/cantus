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

// previewAudioRouter wires a chi router with the PreviewAudio handler.
func previewAudioRouter(signer *services.Signer, storage services.Storage) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/preview-audio/{videoId}", handlers.PreviewAudio(signer, storage))
	return r
}

// newPreviewAudioSigner returns a Signer for tests (32 'p' bytes key).
func newPreviewAudioSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("p", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// newPreviewAudioStorage returns a LocalDiskStorage rooted at a temp dir.
func newPreviewAudioStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// stagePreviewAudioMp3 pre-stages a preview-stems/no_vocals.wav into storage for
// the given videoID.
func stagePreviewAudioMp3(t *testing.T, storage *services.LocalDiskStorage, videoID string, content []byte) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "no_vocals.wav")
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := storage.Commit(context.Background(), storage.Key(videoID, "preview-stems/no_vocals.wav"), tmp); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestPreviewAudioHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewAudioSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name  string
		url   string
		setup func(t *testing.T, st *services.LocalDiskStorage)

		wantStatus       int
		wantBody         string
		wantBodyContains string
	}{
		{
			name: "200 cache hit — body roundtrip",
			url:  "/api/preview-audio/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T, st *services.LocalDiskStorage) {
				stagePreviewAudioMp3(t, st, validID, []byte("fake mp3 preview bytes"))
			},
			wantStatus: http.StatusOK,
			wantBody:   "fake mp3 preview bytes",
		},
		{
			name:             "404 cache miss — preview-stems not generated",
			url:              "/api/preview-audio/" + validID + "?sig=" + validSig,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusNotFound,
			wantBodyContains: "preview not generated",
		},
		{
			name:             "400 invalid videoID",
			url:              "/api/preview-audio/short?sig=anything",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name:             "400 missing sig",
			url:              "/api/preview-audio/" + validID,
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name:             "400 invalid sig",
			url:              "/api/preview-audio/" + validID + "?sig=deadbeef",
			setup:            func(t *testing.T, st *services.LocalDiskStorage) {},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newPreviewAudioStorage(t)
			tt.setup(t, st)
			router := previewAudioRouter(signer, st)

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

// TestPreviewAudioHandler_StorageError tests that storage.Has failure returns 500.
func TestPreviewAudioHandler_StorageError(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewAudioSigner(t)
	validSig := signer.Sign(validID)

	real := newPreviewAudioStorage(t)
	st := &errStorage{Storage: real, errOnHasName: "preview-stems/no_vocals.wav"}
	router := previewAudioRouter(signer, st)

	req := httptest.NewRequest(http.MethodGet, "/api/preview-audio/"+validID+"?sig="+validSig, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusInternalServerError {
		t.Errorf("status: got %d, want %d (body: %s)", got, http.StatusInternalServerError, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "storage check failed") {
		t.Errorf("body: got %q, want it to contain %q", body, "storage check failed")
	}
}

// TestPreviewAudioHandler_RangeRequest verifies http.ServeContent provides Range support.
func TestPreviewAudioHandler_RangeRequest(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newPreviewAudioSigner(t)
	validSig := signer.Sign(validID)

	st := newPreviewAudioStorage(t)
	content := make([]byte, 50)
	for i := range content {
		content[i] = byte(i)
	}
	stagePreviewAudioMp3(t, st, validID, content)

	router := previewAudioRouter(signer, st)

	req := httptest.NewRequest(http.MethodGet, "/api/preview-audio/"+validID+"?sig="+validSig, nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusPartialContent {
		t.Errorf("status: got %d, want 206 (body: %s)", got, rec.Body.String())
	}
	if got := len(rec.Body.Bytes()); got != 10 {
		t.Errorf("body length: got %d, want 10", got)
	}
}
