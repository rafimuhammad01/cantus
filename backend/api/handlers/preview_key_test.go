package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/services"
)

// previewKeyRouter wires a chi router with the PreviewKey handler at /api/preview-key/{videoId}.
func previewKeyRouter(signer *services.Signer, storage services.Storage) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/preview-key/{videoId}", handlers.PreviewKey(signer, storage))
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

// commitKeyFile writes content into storage at the given key for pre-staging test data.
func commitKeyFile(t *testing.T, st *services.LocalDiskStorage, videoID, name string, content []byte) {
	t.Helper()
	commitToStorage(t, st, st.Key(videoID, name), content)
}

func TestPreviewKeyHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
		url  string

		// setup configures storage before the request.
		setup func(t *testing.T) services.Storage

		wantStatus       int
		wantBodyContains string
		wantKey          string
		wantCachedAfter  bool // assert preview-key.json is in storage after request
	}{
		{
			name: "melody.json present returns its key",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) services.Storage {
				st := newKeyStorage(t)
				// Pre-stage melody.json (CREPE on isolated full-song vocals — the one accurate source).
				commitKeyFile(t, st, validID, "melody.json", []byte(`{"key":"F major"}`))
				return st
			},
			wantStatus: http.StatusOK,
			wantKey:    "F major",
		},
		{
			name: "melody.json absent returns empty key (UI hides the label)",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) services.Storage {
				// Empty storage — no melody.json (song hasn't been generated yet).
				return newKeyStorage(t)
			},
			wantStatus: http.StatusOK,
			wantKey:    "",
		},
		{
			name: "melody.json with empty key field returns empty key",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) services.Storage {
				st := newKeyStorage(t)
				commitKeyFile(t, st, validID, "melody.json", []byte(`{"key":""}`))
				return st
			},
			wantStatus: http.StatusOK,
			wantKey:    "",
		},
		{
			name: "invalid videoID",
			url:  "/api/preview-key/bad!!!id?sig=anything",
			setup: func(t *testing.T) services.Storage {
				return newKeyStorage(t)
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name: "invalid sig",
			url:  "/api/preview-key/" + validID + "?sig=deadbeef",
			setup: func(t *testing.T) services.Storage {
				return newKeyStorage(t)
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name: "missing sig",
			url:  "/api/preview-key/" + validID,
			setup: func(t *testing.T) services.Storage {
				return newKeyStorage(t)
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name: "malformed melody.json returns 500",
			url:  "/api/preview-key/" + validID + "?sig=" + validSig,
			setup: func(t *testing.T) services.Storage {
				st := newKeyStorage(t)
				commitKeyFile(t, st, validID, "melody.json", []byte(`not-json{`))
				return st
			},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: "melody parse failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.setup(t)
			router := previewKeyRouter(signer, st)

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

			if tt.wantCachedAfter && rec.Code == http.StatusOK {
				lStorage := st.(*services.LocalDiskStorage)
				ok, err := lStorage.Has(context.Background(), lStorage.Key(validID, "preview-key.json"))
				if err != nil {
					t.Errorf("storage.Has(preview-key.json) after request: %v", err)
				} else if !ok {
					t.Errorf("storage.Has(preview-key.json): got false, want true — Commit did not run")
				}
			}
		})
	}
}
