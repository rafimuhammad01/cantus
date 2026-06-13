package handlers_test

import (
	"context"
	"errors"
	"io"
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

// fakeStorage is a test double for services.Storage.
// It delegates Open to a real LocalDiskStorage when openPath is set, so
// handlers that call storage.Open after a cache-hit can serve the file.
type fakeStorage struct {
	hasResult bool
	hasErr    error
	hasCalled int
	openPath  string // if non-empty, Open returns an os.File for this path
	openErr   error
}

func (f *fakeStorage) Key(_, name string) string { return name }

func (f *fakeStorage) Has(_ context.Context, _ string) (bool, error) {
	f.hasCalled++
	return f.hasResult, f.hasErr
}

func (f *fakeStorage) SignGet(_ context.Context, _ string) (string, error) { return "", nil }
func (f *fakeStorage) SignPut(_ context.Context, _ string) (string, error) { return "", nil }

func (f *fakeStorage) Commit(_ context.Context, _, _ string) error { return nil }

func (f *fakeStorage) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.openPath != "" {
		return os.Open(f.openPath)
	}
	return nil, errors.New("fakeStorage: Open not configured")
}

// fakeYouTubePreview is a test double for the preview-relevant parts of YouTubeService.
type fakeYouTubePreview struct {
	downloadErr     error
	downloadCalled  int
	downloadVideoID string
	// onDownload is optional; called when DownloadPreview is invoked (e.g. to write a file).
	onDownload func(videoID string)
}

func (f *fakeYouTubePreview) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}

func (f *fakeYouTubePreview) DownloadPreview(_ context.Context, videoID string) error {
	f.downloadCalled++
	f.downloadVideoID = videoID
	if f.onDownload != nil {
		f.onDownload(videoID)
	}
	return f.downloadErr
}

func (f *fakeYouTubePreview) DownloadFull(_ context.Context, _ string) error { return nil }

// previewRouter wires a chi router with the Preview handler at /api/preview/{videoId}.
func previewRouter(signer *services.Signer, storage services.Storage, svc services.YouTubeService) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/preview/{videoId}", handlers.Preview(signer, storage, svc))
	return r
}

// newSigner builds a test Signer with a 32-byte key of 'x'.
func newSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// writeTempFile writes content to a temp file in dir and returns its path.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestPreviewHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ" // 11 chars, valid YouTube ID

	signer := newSigner(t)
	validSig := signer.Sign(validID)

	tmpDir := t.TempDir()
	cacheFile := writeTempFile(t, tmpDir, "preview.mp3", "fake mp3 bytes")

	tests := []struct {
		name string
		url  string

		storage *fakeStorage
		svc     *fakeYouTubePreview

		wantStatus          int
		wantBodyContains    string
		wantBody            string
		wantHasCalled       int
		wantDownloadCalled  int
		wantDownloadVideoID string
		wantContentType     string // if non-empty, check Content-Type starts with this
	}{
		{
			name:               "invalid videoID — too short",
			url:                "/api/preview/short?sig=anything",
			storage:            &fakeStorage{},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid videoId",
			wantHasCalled:      0,
			wantDownloadCalled: 0,
		},
		{
			name:               "invalid videoID — contains illegal chars",
			url:                "/api/preview/bad!!!chars?sig=anything",
			storage:            &fakeStorage{},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid videoId",
			wantHasCalled:      0,
			wantDownloadCalled: 0,
		},
		{
			name:               "valid videoID but bad sig",
			url:                "/api/preview/" + validID + "?sig=deadbeef",
			storage:            &fakeStorage{},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid sig",
			wantHasCalled:      0,
			wantDownloadCalled: 0,
		},
		{
			name:               "valid videoID but missing sig",
			url:                "/api/preview/" + validID,
			storage:            &fakeStorage{},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid sig",
			wantHasCalled:      0,
			wantDownloadCalled: 0,
		},
		{
			name: "cache hit happy path",
			url:  "/api/preview/" + validID + "?sig=" + validSig,
			storage: &fakeStorage{
				hasResult: true,
				openPath:  cacheFile,
			},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusOK,
			wantBody:           "fake mp3 bytes",
			wantHasCalled:      1,
			wantDownloadCalled: 0,
		},
		{
			name: "cache miss — download — serve",
			url:  "/api/preview/" + validID + "?sig=" + validSig,
			// storage and svc built together below so they share the missPath.
			storage:             nil,
			svc:                 nil,
			wantStatus:          http.StatusOK,
			wantBody:            "downloaded mp3",
			wantHasCalled:       1,
			wantDownloadCalled:  1,
			wantDownloadVideoID: validID,
		},
		{
			name: "storage.Has returns error",
			url:  "/api/preview/" + validID + "?sig=" + validSig,
			storage: &fakeStorage{
				hasErr: errors.New("disk error"),
			},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusInternalServerError,
			wantBodyContains:   "storage check failed",
			wantHasCalled:      1,
			wantDownloadCalled: 0,
		},
		{
			name: "DownloadPreview returns error",
			url:  "/api/preview/" + validID + "?sig=" + validSig,
			storage: &fakeStorage{
				hasResult: false,
			},
			svc:                &fakeYouTubePreview{downloadErr: errors.New("network down")},
			wantStatus:         http.StatusBadGateway,
			wantBodyContains:   "download failed",
			wantHasCalled:      1,
			wantDownloadCalled: 1,
		},
		{
			name: "range request — 206 partial content",
			url:  "/api/preview/" + validID + "?sig=" + validSig,
			storage: &fakeStorage{
				hasResult: true,
				openPath:  cacheFile,
			},
			svc:                &fakeYouTubePreview{},
			wantStatus:         http.StatusPartialContent,
			wantHasCalled:      1,
			wantDownloadCalled: 0,
		},
	}

	// Build the cache-miss test case: fakeStorage and fakeYouTubePreview must share
	// the same path so the download callback writes the file that Open will return.
	missingIdx := -1
	for i, tt := range tests {
		if tt.name == "cache miss — download — serve" {
			missingIdx = i
			break
		}
	}
	if missingIdx >= 0 {
		missPath := filepath.Join(t.TempDir(), "preview.mp3")
		missStorage := &fakeStorage{
			hasResult: false,
			openPath:  missPath,
		}
		missSvc := &fakeYouTubePreview{
			onDownload: func(_ string) {
				// Simulate the real DownloadPreview: write the file so Open can serve it.
				_ = os.WriteFile(missPath, []byte("downloaded mp3"), 0o600)
			},
		}
		tests[missingIdx].storage = missStorage
		tests[missingIdx].svc = missSvc
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := previewRouter(signer, tt.storage, tt.svc)

			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			if tt.name == "range request — 206 partial content" {
				req.Header.Set("Range", "bytes=0-4")
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got, want := rec.Code, tt.wantStatus; got != want {
				t.Errorf("status: got %d, want %d", got, want)
			}

			if tt.wantBodyContains != "" {
				body := rec.Body.String()
				if !strings.Contains(body, tt.wantBodyContains) {
					t.Errorf("body: got %q, want it to contain %q", body, tt.wantBodyContains)
				}
			}

			if tt.wantBody != "" {
				body := rec.Body.String()
				if body != tt.wantBody {
					t.Errorf("body: got %q, want %q", body, tt.wantBody)
				}
			}

			if got, want := tt.storage.hasCalled, tt.wantHasCalled; got != want {
				t.Errorf("storage.Has call count: got %d, want %d", got, want)
			}

			if got, want := tt.svc.downloadCalled, tt.wantDownloadCalled; got != want {
				t.Errorf("svc.DownloadPreview call count: got %d, want %d", got, want)
			}

			if tt.wantDownloadVideoID != "" {
				if got, want := tt.svc.downloadVideoID, tt.wantDownloadVideoID; got != want {
					t.Errorf("svc.DownloadPreview videoID: got %q, want %q", got, want)
				}
			}
		})
	}
}
