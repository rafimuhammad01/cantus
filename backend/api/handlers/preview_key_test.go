package handlers_test

import (
	"context"
	"encoding/json"
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

// fakeYouTubeKey is a test double for services.YouTubeService, used in preview_key tests.
type fakeYouTubeKey struct {
	onDownload func(videoID string)
	err        error
	callCount  int
}

func (f *fakeYouTubeKey) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}

func (f *fakeYouTubeKey) DownloadPreview(_ context.Context, videoID string) error {
	f.callCount++
	if f.onDownload != nil {
		f.onDownload(videoID)
	}
	return f.err
}

func (f *fakeYouTubeKey) DownloadFull(_ context.Context, _ string) error { return nil }

// fakeProcKey is a test double for services.ProcessorClient, used in preview_key tests.
type fakeProcKey struct {
	key       string
	err       error
	callCount int
	lastPath  string
}

func (f *fakeProcKey) Shift(_ context.Context, _, _ string, _ float64) error { return nil }
func (f *fakeProcKey) Separate(_ context.Context, _, _ string) (string, string, error) {
	return "", "", nil
}
func (f *fakeProcKey) Melody(_ context.Context, _, _ string) error { return nil }
func (f *fakeProcKey) PreviewKey(_ context.Context, path string) (string, error) {
	f.callCount++
	f.lastPath = path
	return f.key, f.err
}

// previewKeyRouter wires a chi router with the PreviewKey handler at /api/preview-key/{videoId}.
func previewKeyRouter(signer *services.Signer, storage services.Storage, yt services.YouTubeService, proc services.ProcessorClient) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/preview-key/{videoId}", handlers.PreviewKey(signer, storage, yt, proc))
	return r
}

// newKeyStorage returns a LocalDiskStorage rooted at a temp dir.
func newKeyStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

func TestPreviewKeyHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
		url  string

		// setup configures storage, fake YouTube, and fake processor before the request.
		setup func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey)

		wantStatus        int
		wantBodyContains  string
		wantKey           string
		wantYTCallCount   int
		wantProcCallCount int
		wantCachedAfter   bool // assert preview-key.json is in storage after request
	}{
		{
			name: "melody.json present returns its key",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				st := newKeyStorage(t)
				// Pre-stage melody.json (CREPE on isolated full-song vocals — the one accurate source).
				melodyPath := st.FilesystemPathForLocalProcessor(st.Key(validID, "melody.json"))
				_ = os.MkdirAll(filepath.Dir(melodyPath), 0o755)
				_ = os.WriteFile(melodyPath, []byte(`{"key":"F major"}`), 0o644)
				return st, &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusOK,
			wantKey:           "F major",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "melody.json absent returns empty key (UI hides the label)",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				// Empty storage — no melody.json (song hasn't been generated yet).
				return newKeyStorage(t), &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusOK,
			wantKey:           "",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "melody.json with empty key field returns empty key",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				st := newKeyStorage(t)
				melodyPath := st.FilesystemPathForLocalProcessor(st.Key(validID, "melody.json"))
				_ = os.MkdirAll(filepath.Dir(melodyPath), 0o755)
				_ = os.WriteFile(melodyPath, []byte(`{"key":""}`), 0o644)
				return st, &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusOK,
			wantKey:           "",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "invalid videoID",
			url:  "/api/preview-key/bad!!!id?sig=anything",
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				return newKeyStorage(t), &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusBadRequest,
			wantBodyContains:  "invalid videoId",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "invalid sig",
			url:  "/api/preview-key/" + validID + "?sig=deadbeef",
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				return newKeyStorage(t), &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusBadRequest,
			wantBodyContains:  "invalid sig",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "missing sig",
			url:  "/api/preview-key/" + validID,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				return newKeyStorage(t), &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusBadRequest,
			wantBodyContains:  "invalid sig",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
		{
			name: "malformed melody.json returns 500",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeKey, *fakeProcKey) {
				st := newKeyStorage(t)
				melodyPath := st.FilesystemPathForLocalProcessor(st.Key(validID, "melody.json"))
				_ = os.MkdirAll(filepath.Dir(melodyPath), 0o755)
				_ = os.WriteFile(melodyPath, []byte(`not-json{`), 0o644)
				return st, &fakeYouTubeKey{}, &fakeProcKey{}
			},
			wantStatus:        http.StatusInternalServerError,
			wantBodyContains:  "melody parse failed",
			wantYTCallCount:   0,
			wantProcCallCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, yt, proc := tt.setup(t)
			router := previewKeyRouter(signer, st, yt, proc)

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

			if rec.Code == http.StatusOK {
				var resp struct {
					Key string `json:"key"`
				}
				if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response body: %v", err)
				}
				if resp.Key != tt.wantKey {
					t.Errorf("key: got %q, want %q", resp.Key, tt.wantKey)
				}
			}

			if got, want := yt.callCount, tt.wantYTCallCount; got != want {
				t.Errorf("DownloadPreview call count: got %d, want %d", got, want)
			}

			if got, want := proc.callCount, tt.wantProcCallCount; got != want {
				t.Errorf("PreviewKey call count: got %d, want %d", got, want)
			}

			if tt.wantCachedAfter && rec.Code == http.StatusOK {
				ok, err := st.Has(context.Background(), st.Key(validID, "preview-key.json"))
				if err != nil {
					t.Errorf("storage.Has(preview-key.json) after request: %v", err)
				} else if !ok {
					t.Errorf("storage.Has(preview-key.json): got false, want true — Commit did not run")
				}
			}
		})
	}
}
