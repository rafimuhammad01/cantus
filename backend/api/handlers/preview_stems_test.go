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
// preview_stems tests for Stage 4 (Melody) which still uses the old interface.
type fakeStemsProcessor struct {
	melodyErr   error
	melodyCount int
	// onMelody simulates CREPE writing melody.json to outputPath.
	onMelody func(outputPath string)
}

func (f *fakeStemsProcessor) Shift(_ context.Context, _, _ string, _ float64) error { return nil }
func (f *fakeStemsProcessor) PreviewKey(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeStemsProcessor) Separate(_ context.Context, _, outputDir string) (string, string, error) {
	return filepath.Join(outputDir, "vocals.wav"), filepath.Join(outputDir, "no_vocals.wav"), nil
}

func (f *fakeStemsProcessor) Melody(_ context.Context, _, outputPath string) error {
	f.melodyCount++
	if f.onMelody != nil {
		f.onMelody(outputPath)
	}
	return f.melodyErr
}

// fakeGPUStemsProcessor is a test double for services.GPUProcessorClient used in
// preview_stems tests for Stage 2 (Separate) and Stage 4 (Melody via URL handoff).
type fakeGPUStemsProcessor struct {
	separateErr   error
	separateCount int
	melodyErr     error
	melodyCount   int
	// onSeparate is called after the fake records the call; use it to commit stems into storage.
	onSeparate func()
	// onMelody is called after the fake records the call; use it to commit melody.json into storage.
	onMelody func()
}

func (f *fakeGPUStemsProcessor) Separate(_ context.Context, _, _, _ string) error {
	f.separateCount++
	if f.onSeparate != nil {
		f.onSeparate()
	}
	return f.separateErr
}

func (f *fakeGPUStemsProcessor) Melody(_ context.Context, _, _ string) error {
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
	proc services.ProcessorClient,
	gpu services.GPUProcessorClient,
	transcode services.TranscodeFunc,
) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/preview-stems", handlers.PreviewStems(signer, storage, yt, proc, gpu, transcode))
	return r
}

// fakeTranscode writes a fixed sentinel into outputPath. It records whether it was called.
func makeFakeTranscode(writeContent []byte, err error) (services.TranscodeFunc, *int) {
	count := new(int)
	fn := func(_ context.Context, _, outputPath string) error {
		*count++
		if err != nil {
			return err
		}
		return os.WriteFile(outputPath, writeContent, 0o644)
	}
	return fn, count
}

// preStagePreview writes a dummy preview.mp3 into storage for the given videoID.
func preStagePreview(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	p := st.FilesystemPathForLocalProcessor(st.Key(videoID, "preview.mp3"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte("fake preview bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile preview.mp3: %v", err)
	}
}

// preStageStems writes dummy vocals.wav + no_vocals.wav into storage.
func preStageStems(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	for _, name := range []string{"preview-stems/vocals.wav", "preview-stems/no_vocals.wav"} {
		p := st.FilesystemPathForLocalProcessor(st.Key(videoID, name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("fake wav"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
}

// preStageMp3 writes a dummy no_vocals.mp3 into storage.
func preStageMp3(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	p := st.FilesystemPathForLocalProcessor(st.Key(videoID, "preview-stems/no_vocals.mp3"))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("fake mp3"), 0o644)
}

// preStageMelody writes a dummy melody.json into storage.
func preStageMelody(t *testing.T, st *services.LocalDiskStorage, videoID string) {
	t.Helper()
	p := st.FilesystemPathForLocalProcessor(st.Key(videoID, "preview-stems/melody.json"))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(`{"notes":[]}`), 0o644)
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
	for _, name := range []string{"preview-stems/vocals.wav", "preview-stems/no_vocals.wav"} {
		src := filepath.Join(t.TempDir(), "stem.wav")
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

		// setup returns storage, fake yt, fake proc (melody), fake gpu (separate), fake transcode.
		setup func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int)

		wantStatus       int
		wantBodyContains string
		wantReady        bool
		wantDownload     int
		wantSeparate     int
		wantTranscode    int
		wantMelody       int
		// names to assert are cached after a successful request
		wantCached []string
	}{
		{
			name: "happy path, cold cache — all stages run",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				yt := &fakeYouTubeStems{
					onDownload: func(videoID string) {
						preStagePreview(t, st, videoID)
					},
				}
				gpu := &fakeGPUStemsProcessor{
					onSeparate: func() {
						commitStemsToStorage(t, st, validID)
					},
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				proc := &fakeStemsProcessor{}
				transcode, count := makeFakeTranscode([]byte("fake mp3"), nil)
				return st, yt, proc, gpu, transcode, count
			},
			wantStatus:    http.StatusOK,
			wantReady:     true,
			wantDownload:  1,
			wantSeparate:  1,
			wantTranscode: 1,
			wantMelody:    1,
			wantCached:    []string{"preview-stems/no_vocals.mp3", "preview-stems/melody.json"},
		},
		{
			name: "idempotent — all artifacts cached, no upstream calls",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				preStageMp3(t, st, validID)
				preStageMelody(t, st, validID)
				transcode, count := makeFakeTranscode(nil, nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:    http.StatusOK,
			wantReady:     true,
			wantDownload:  0,
			wantSeparate:  0,
			wantTranscode: 0,
			wantMelody:    0,
		},
		{
			name: "partial cache — vocals.wav present but melody.json absent — Demucs skipped, Melody runs",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				preStageMp3(t, st, validID)
				// melody.json intentionally absent
				gpu := &fakeGPUStemsProcessor{
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				transcode, count := makeFakeTranscode(nil, nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, gpu, transcode, count
			},
			wantStatus:    http.StatusOK,
			wantReady:     true,
			wantDownload:  0,
			wantSeparate:  0,
			wantTranscode: 0,
			wantMelody:    1,
			wantCached:    []string{"preview-stems/melody.json"},
		},
		{
			name: "partial cache — preview exists, stems absent — Demucs runs",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				// no stems cached
				gpu := &fakeGPUStemsProcessor{
					onSeparate: func() {
						commitStemsToStorage(t, st, validID)
					},
					onMelody: func() {
						commitMelodyToStorage(t, st, validID)
					},
				}
				transcode, count := makeFakeTranscode([]byte("fake mp3"), nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, gpu, transcode, count
			},
			wantStatus:    http.StatusOK,
			wantReady:     true,
			wantDownload:  0,
			wantSeparate:  1,
			wantTranscode: 1,
			wantMelody:    1,
		},
		{
			name: "400 — malformed JSON body",
			body: "not json",
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				transcode, count := makeFakeTranscode(nil, nil)
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid request body",
		},
		{
			name: "400 — invalid videoId",
			body: `{"video_id":"bad/slash!!","sig":"anything"}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				transcode, count := makeFakeTranscode(nil, nil)
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid videoId",
		},
		{
			name: `400 — invalid sig`,
			body: `{"video_id":"` + validID + `","sig":"deadbeef"}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				transcode, count := makeFakeTranscode(nil, nil)
				return newStemsStorage(t), &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: "invalid sig",
		},
		{
			name: "502 — DownloadPreview failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				yt := &fakeYouTubeStems{err: errors.New("yt-dlp died")}
				transcode, count := makeFakeTranscode(nil, nil)
				return st, yt, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusBadGateway,
			wantBodyContains: "download failed",
			wantDownload:     1,
		},
		{
			name: "502 — Separate failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				gpu := &fakeGPUStemsProcessor{separateErr: errors.New("demucs crashed")}
				transcode, count := makeFakeTranscode(nil, nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, gpu, transcode, count
			},
			wantStatus:       http.StatusBadGateway,
			wantBodyContains: "separate failed",
			wantSeparate:     1,
		},
		{
			name: "502 — Transcode failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				transcode, count := makeFakeTranscode(nil, errors.New("ffmpeg not found"))
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusBadGateway,
			wantBodyContains: "transcode failed",
			wantTranscode:    1,
		},
		{
			name: "502 — Melody failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				st := newStemsStorage(t)
				preStagePreview(t, st, validID)
				preStageStems(t, st, validID)
				preStageMp3(t, st, validID)
				gpu := &fakeGPUStemsProcessor{melodyErr: errors.New("crepe exploded")}
				transcode, count := makeFakeTranscode(nil, nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, gpu, transcode, count
			},
			wantStatus:       http.StatusBadGateway,
			wantBodyContains: "melody failed",
			wantMelody:       1,
		},
		{
			name: "500 — storage.Has failure",
			body: stemBody(validID, validSig),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeStems, *fakeStemsProcessor, *fakeGPUStemsProcessor, services.TranscodeFunc, *int) {
				real := newStemsStorage(t)
				// Fail on the first Has call (no_vocals.mp3 check).
				st := &errStorage{Storage: real, errOnHasName: "preview-stems/no_vocals.mp3"}
				transcode, count := makeFakeTranscode(nil, nil)
				return st, &fakeYouTubeStems{}, &fakeStemsProcessor{}, &fakeGPUStemsProcessor{}, transcode, count
			},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: "storage check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, yt, proc, gpu, transcode, transcodeCount := tt.setup(t)
			router := stemsRouter(signer, st, yt, proc, gpu, transcode)

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

			if got, want := *transcodeCount, tt.wantTranscode; got != want {
				t.Errorf("Transcode call count: got %d, want %d", got, want)
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
