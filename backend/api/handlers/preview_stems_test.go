package handlers_test

import (
	"context"
	"errors"
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

// fakeStemsProcessor is a test double for services.ProcessorClient used in
// preview_stems tests for Stage 2 (Separate) and Stage 3 (Melody via URL handoff).
type fakeStemsProcessor struct {
	separateErr   error
	separateCount int
	melodyErr     error
	melodyCount   int
	// onSeparate is called after the fake records the call; use it to commit stems into storage.
	onSeparate func()
	// onMelody is called after the fake records the call; use it to commit melody.json into storage.
	onMelody func()
}

func (f *fakeStemsProcessor) Separate(_ context.Context, _, _, _ string) error {
	f.separateCount++
	if f.onSeparate != nil {
		f.onSeparate()
	}
	return f.separateErr
}

func (f *fakeStemsProcessor) Melody(_ context.Context, _, _ string) error {
	f.melodyCount++
	if f.onMelody != nil {
		f.onMelody()
	}
	return f.melodyErr
}

// fakeYouTubeStems is a test double for services.YouTubeService in preview_stems tests.
type fakeYouTubeStems struct {
	err        error
	callCount  int
	onDownload func(videoID string)
}

func (f *fakeYouTubeStems) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}

func (f *fakeYouTubeStems) DownloadPreview(_ context.Context, videoID string) error {
	f.callCount++
	if f.onDownload != nil {
		f.onDownload(videoID)
	}
	return f.err
}

func (f *fakeYouTubeStems) DownloadFull(_ context.Context, _ string) error { return nil }

// errStorage wraps a real LocalDiskStorage but returns an error on Has for a
// specific key suffix, used to test 500 paths. errOnHasName is matched against
// the tail of the key (i.e. the "name" portion after the videoID prefix).
type errStorage struct {
	services.Storage
	errOnHasName string // if non-empty, Has returns an error when the key ends with this
}

func (e *errStorage) Has(ctx context.Context, key string) (bool, error) {
	if e.errOnHasName != "" && len(key) >= len(e.errOnHasName) &&
		key[len(key)-len(e.errOnHasName):] == e.errOnHasName {
		return false, errors.New("storage exploded")
	}
	return e.Storage.Has(ctx, key)
}

// newStemsStorage returns a LocalDiskStorage rooted at a temp dir.
func newStemsStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// newStemsSigner returns a Signer for tests (32 's' bytes key).
func newStemsSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// stemBody builds the JSON body for a POST /api/preview-stems request.
func stemBody(videoID, sig string) string {
	return `{"video_id":"` + videoID + `","sig":"` + sig + `"}`
}

// stemsRouter wires a chi router with the PreviewStems handler.
func stemsRouter(
	signer *services.Signer,
	storage services.Storage,
	yt services.YouTubeService,
	processor services.ProcessorClient,
) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/preview-stems", handlers.PreviewStems(signer, storage, yt, processor, services.NewVideoFailureTracker(), nil))
	return r
}

// preStagePreview writes a dummy preview.wav into storage for the given videoID.
func preStagePreview(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	commitToStorage(t, st, st.Key(videoID, "preview"+services.AudioExt), []byte("fake preview bytes"))
}

// preStageStems writes dummy vocals.wav + no_vocals.wav into storage.
func preStageStems(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	for _, name := range []string{"preview-stems/vocals" + services.AudioExt, "preview-stems/no_vocals" + services.AudioExt} {
		commitToStorage(t, st, st.Key(videoID, name), []byte("fake wav"))
	}
}

// preStageMelody writes a dummy melody.json into storage.
func preStageMelody(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	commitToStorage(t, st, st.Key(videoID, "preview-stems/melody.json"), []byte(`{"notes":[]}`))
}

// commitMelodyToStorage writes melody.json directly into storage via Commit
// so that storage.Verify passes after the fake GPU Melody call.
func commitMelodyToStorage(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "melody.json")
	if err := os.WriteFile(src, []byte(`{"notes":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := st.Commit(context.Background(), st.Key(videoID, "preview-stems/melody.json"), src); err != nil {
		t.Fatalf("Commit(melody.json): %v", err)
	}
}

// commitStemsToStorage writes vocals.wav + no_vocals.wav directly into storage via Commit
// so that storage.Verify passes after the fake GPU Separate call.
func commitStemsToStorage(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	for _, name := range []string{"preview-stems/vocals" + services.AudioExt, "preview-stems/no_vocals" + services.AudioExt} {
		src := filepath.Join(t.TempDir(), "stem"+services.AudioExt)
		if err := os.WriteFile(src, []byte("fake wav"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := st.Commit(context.Background(), st.Key(videoID, name), src); err != nil {
			t.Fatalf("Commit(%s): %v", name, err)
		}
	}
}

func TestPreviewStemsHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newStemsSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
		body string

		// setup returns storage, fake yt, fake gpu.
		setup func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor)

		wantStatus       int
		wantBodyContains string
		wantReady        bool
		wantDownload     int
		wantSeparate     int
		wantMelody       int
		// names to assert are cached after a successful request
		wantCached []string
	}{
		{
			name: "happy path, cold cache — all stages run",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				yt := &fakeYouTubeStems{
					onDownload: func(videoID string) {
						preStagePreview(t, st, videoID)
					},
				}
				gpu := &fakeStemsProcessor{
					onSeparate: func() {
						commitStemsToStorage(t, st, validID)
					},
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				return st, yt, gpu
			},
			wantStatus:   http.StatusOK,
			wantReady:    true,
			wantDownload: 1,
			wantSeparate: 1,
			wantMelody:   1,
			wantCached:   []string{"preview-stems/no_vocals" + services.AudioExt, "preview-stems/melody.json"},
		},
		{
			name: "idempotent — all artifacts cached, no upstream calls",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				preStageMelody(t, st, validID)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}
			},
			wantStatus:   http.StatusOK,
			wantReady:    true,
			wantDownload: 0,
			wantSeparate: 0,
			wantMelody:   0,
		},
		{
			name: "partial cache — vocals.wav present but melody.json absent — Demucs skipped, Melody runs",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				// melody.json intentionally absent
				gpu := &fakeStemsProcessor{
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				return st, &fakeYouTubeStems{}, gpu
			},
			wantStatus:   http.StatusOK,
			wantReady:    true,
			wantDownload: 0,
			wantSeparate: 0,
			wantMelody:   1,
			wantCached:   []string{"preview-stems/melody.json"},
		},
		{
			name: "partial cache — preview exists, stems absent — Demucs runs",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				// no stems cached
				gpu := &fakeStemsProcessor{
					onSeparate: func() {
						commitStemsToStorage(t, st, validID)
					},
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				return st, &fakeYouTubeStems{}, gpu
			},
			wantStatus:   http.StatusOK,
			wantReady:    true,
			wantDownload: 0,
			wantSeparate: 1,
			wantMelody:   1,
		},
		{
			name: "400 — malformed JSON body",
			body: "not json",
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid request body",
		},
		{
			name: "400 — invalid videoId",
			body: `{"video_id":"bad/slash!!","sig":"anything"}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name: `400 — invalid sig`,
			body: `{"video_id":"` + validID + `","sig":"deadbeef"}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name: "200 streaming — DownloadPreview failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				yt := &fakeYouTubeStems{err: errors.New("yt-dlp died")}
				return st, yt, &fakeStemsProcessor{}
			},
			wantStatus:       http.StatusOK,
			wantBodyContains: "download failed",
			wantDownload:     services.PipelineRetryAttempts,
		},
		{
			name: "200 streaming — Separate failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				gpu := &fakeStemsProcessor{separateErr: errors.New("demucs crashed")}
				return st, &fakeYouTubeStems{}, gpu
			},
			wantStatus:       http.StatusOK,
			wantBodyContains: "separate failed",
			wantSeparate:     services.PipelineRetryAttempts,
		},
		{
			name: "200 streaming — Melody failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				gpu := &fakeStemsProcessor{melodyErr: errors.New("crepe exploded")}
				return st, &fakeYouTubeStems{}, gpu
			},
			wantStatus:       http.StatusOK,
			wantBodyContains: "melody failed",
			wantMelody:       services.PipelineRetryAttempts,
		},
		{
			name: "500 — storage.Has failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor) {
				real := newStemsStorage(t)
				// Fail on the first Has call (no_vocals.wav check) — this is in Phase 2 (pre-streaming).
				st := &errStorage{Storage: real, errOnHasName: "preview-stems/no_vocals" + services.AudioExt}
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}
			},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: "storage check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, yt, gpu := tt.setup(t)
			router := stemsRouter(signer, st, yt, gpu)

			req := httptest.NewRequest(http.MethodPost, "/api/preview-stems", strings.NewReader(tt.body))
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

			if tt.wantReady {
				body := rec.Body.String()
				if !strings.Contains(body, `"ready":true`) {
					t.Errorf("body: got %q, want it to contain %q", body, `"ready":true`)
				}
			}

			if got, want := yt.callCount, tt.wantDownload; got != want {
				t.Errorf("DownloadPreview call count: got %d, want %d", got, want)
			}

			if got, want := gpu.separateCount, tt.wantSeparate; got != want {
				t.Errorf("Separate call count: got %d, want %d", got, want)
			}

			if got, want := gpu.melodyCount, tt.wantMelody; got != want {
				t.Errorf("Melody call count: got %d, want %d", got, want)
			}

			for _, name := range tt.wantCached {
				if realSt, ok := st.(*services.LocalDiskStorage); ok {
					ok2, err := realSt.Has(context.Background(), realSt.Key(validID, name))
					if err != nil {
						t.Errorf("storage.Has(%q) after request: %v", name, err)
					} else if !ok2 {
						t.Errorf("storage.Has(%q): got false, want true — artifact not committed", name)
					}
				}
			}
		})
	}
}
