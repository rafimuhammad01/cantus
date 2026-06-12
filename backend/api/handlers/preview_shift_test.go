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
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/services"
)

// fakeProcessor is a test double for services.ProcessorClient.
type fakeProcessor struct {
	err        error
	writeBytes []byte
	callCount  int
	lastInput  string
	lastOutput string
	lastSemi   float64
}

func (f *fakeProcessor) Separate(_ context.Context, _, _ string) (string, string, error) {
	return "", "", nil
}

func (f *fakeProcessor) Melody(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeProcessor) Shift(_ context.Context, inputPath, outputPath string, semitones float64) error {
	f.callCount++
	f.lastInput, f.lastOutput, f.lastSemi = inputPath, outputPath, semitones
	if f.err != nil {
		return f.err
	}
	if f.writeBytes != nil {
		return os.WriteFile(outputPath, f.writeBytes, 0o644)
	}
	return nil
}

func (f *fakeProcessor) PreviewKey(_ context.Context, _ string) (string, error) { return "", nil }

// fakeYouTubeShift is a test double for services.YouTubeService, used in preview_shift tests.
// DownloadPreview optionally calls onDownload to simulate writing the preview file.
type fakeYouTubeShift struct {
	err        error
	callCount  int
	onDownload func(videoID string)
}

func (f *fakeYouTubeShift) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}

func (f *fakeYouTubeShift) DownloadPreview(_ context.Context, videoID string) error {
	f.callCount++
	if f.onDownload != nil {
		f.onDownload(videoID)
	}
	return f.err
}

func (f *fakeYouTubeShift) DownloadFull(_ context.Context, _ string) error { return nil }

// newErrStorage returns an errStorage that errors on Has for the given name.
func newErrStorage(t *testing.T, errOnName string) *errStorage {
	t.Helper()
	real := newRealStorage(t)
	return &errStorage{Storage: real, errOnHasName: errOnName}
}

// shiftRouter wires a chi router with the PreviewShift handler at /api/preview-shift.
func shiftRouter(signer *services.Signer, storage services.Storage, yt services.YouTubeService, proc services.ProcessorClient) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/preview-shift", handlers.PreviewShift(signer, storage, yt, proc))
	return r
}

// newShiftSigner returns a Signer for tests (32 'x' bytes key).
func newShiftSigner(t *testing.T) *services.Signer {
	t.Helper()
	s, err := services.NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("services.NewSigner: %v", err)
	}
	return s
}

// newRealStorage returns a LocalDiskStorage rooted at a temp dir.
func newRealStorage(t *testing.T) *services.LocalDiskStorage {
	t.Helper()
	st, err := services.NewLocalDiskStorage(t.TempDir(), 1*time.Hour)
	if err != nil {
		t.Fatalf("services.NewLocalDiskStorage: %v", err)
	}
	return st
}

// shiftBody builds the JSON body for a POST /api/preview-shift request.
func shiftBody(videoID, sig string, semitones int) string {
	return `{"video_id":"` + videoID + `","sig":"` + sig + `","semitones":` + itoa(semitones) + `}`
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestPreviewShiftHandler(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newShiftSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
		body string

		// setup is called before the request to configure fakes / storage.
		setup func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor)

		wantStatus         int
		wantBody           string
		wantBodyContains   string
		wantDownloadCalled int
		wantShiftCalled    int
		wantShiftSemitones float64
		wantInputSuffix    string // lastInput must end with this
		wantCached         string // if non-empty, assert storage.Has returns true for this name after request
		wantContentTypeAny bool   // assert Content-Type starts with audio/ or application/octet-stream
	}{
		{
			name: "happy path, cache miss, no preview yet",
			body: shiftBody(validID, validSig, -2),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				proc := &fakeProcessor{writeBytes: []byte("fake shifted bytes")}
				yt := &fakeYouTubeShift{
					onDownload: func(videoID string) {
						previewPath, _ := st.LocalPath(context.Background(), videoID, "preview.mp3")
						_ = os.MkdirAll(filepath.Dir(previewPath), 0o755)
						_ = os.WriteFile(previewPath, []byte("fake preview bytes"), 0o644)
					},
				}
				return st, yt, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "fake shifted bytes",
			wantDownloadCalled: 1,
			wantShiftCalled:    1,
			wantShiftSemitones: -2.0,
			wantInputSuffix:    "/preview.mp3",
			wantCached:         "preview-shifts/-2.mp3",
			wantContentTypeAny: true,
		},
		{
			name: "happy path, preview already cached",
			body: shiftBody(validID, validSig, 3),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write preview.mp3 into storage.
				previewPath, _ := st.LocalPath(context.Background(), validID, "preview.mp3")
				_ = os.MkdirAll(filepath.Dir(previewPath), 0o755)
				_ = os.WriteFile(previewPath, []byte("fake preview bytes"), 0o644)
				proc := &fakeProcessor{writeBytes: []byte("shifted +3")}
				yt := &fakeYouTubeShift{}
				return st, yt, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "shifted +3",
			wantDownloadCalled: 0,
			wantShiftCalled:    1,
			wantShiftSemitones: 3.0,
			wantContentTypeAny: true,
		},
		{
			name: "happy path, shifted already cached",
			body: shiftBody(validID, validSig, -2),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write the shifted file into storage.
				shiftedPath, _ := st.LocalPath(context.Background(), validID, "preview-shifts/-2.mp3")
				_ = os.MkdirAll(filepath.Dir(shiftedPath), 0o755)
				_ = os.WriteFile(shiftedPath, []byte("pre-cached shifted"), 0o644)
				proc := &fakeProcessor{}
				yt := &fakeYouTubeShift{}
				return st, yt, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "pre-cached shifted",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
			wantContentTypeAny: true,
		},
		{
			name: "semitones=0 is valid",
			body: shiftBody(validID, validSig, 0),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				proc := &fakeProcessor{writeBytes: []byte("zero shift")}
				yt := &fakeYouTubeShift{
					onDownload: func(videoID string) {
						p, _ := st.LocalPath(context.Background(), videoID, "preview.mp3")
						_ = os.MkdirAll(filepath.Dir(p), 0o755)
						_ = os.WriteFile(p, []byte("fake preview"), 0o644)
					},
				}
				return st, yt, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "zero shift",
			wantDownloadCalled: 1,
			wantShiftCalled:    1,
			wantShiftSemitones: 0.0,
			wantContentTypeAny: true,
		},
		{
			name: "invalid videoID",
			body: `{"video_id":"bad/slash!!","sig":"anything","semitones":-2}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid videoId",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "bad sig",
			body: `{"video_id":"` + validID + `","sig":"deadbeef","semitones":-2}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid sig",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "missing sig field",
			body: `{"video_id":"` + validID + `","semitones":-2}`,
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid sig",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "semitones=-13 out of range",
			body: shiftBody(validID, validSig, -13),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "semitones must be in [-12, 12]",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "semitones=13 out of range",
			body: shiftBody(validID, validSig, 13),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "semitones must be in [-12, 12]",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "malformed JSON body",
			body: "not json",
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadRequest,
			wantBodyContains:   "invalid request body",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
		{
			name: "DownloadPreview returns error",
			body: shiftBody(validID, validSig, -2),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				yt := &fakeYouTubeShift{err: errors.New("yt-dlp died")}
				return st, yt, &fakeProcessor{}
			},
			wantStatus:         http.StatusBadGateway,
			wantBodyContains:   "download failed",
			wantDownloadCalled: 1,
			wantShiftCalled:    0,
		},
		{
			name: "Shift returns error",
			body: shiftBody(validID, validSig, -2),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write preview so DownloadPreview is skipped.
				p, _ := st.LocalPath(context.Background(), validID, "preview.mp3")
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, []byte("preview"), 0o644)
				proc := &fakeProcessor{err: errors.New("ffmpeg died")}
				return st, &fakeYouTubeShift{}, proc
			},
			wantStatus:         http.StatusBadGateway,
			wantBodyContains:   "shift failed",
			wantDownloadCalled: 0,
			wantShiftCalled:    1,
			wantShiftSemitones: -2.0,
		},

		// --- Stem-path tests (Cycle 3) ---

		{
			name: "stem cache hit — serve stem-shifted without compute",
			body: shiftBody(validID, validSig, -3),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write the stem-shifted file.
				p, _ := st.LocalPath(context.Background(), validID, "preview-stems/shifted/-3.mp3")
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, []byte("stem shifted cached"), 0o644)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusOK,
			wantBody:           "stem shifted cached",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
			wantContentTypeAny: true,
		},
		{
			name: "legacy cache hit, stem-shifted absent — serve legacy",
			body: shiftBody(validID, validSig, 5),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write legacy shifted, no stem-shifted file.
				p, _ := st.LocalPath(context.Background(), validID, "preview-shifts/5.mp3")
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, []byte("legacy shifted cached"), 0o644)
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusOK,
			wantBody:           "legacy shifted cached",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
			wantContentTypeAny: true,
		},
		{
			// REGRESSION: a song that was previewed in a prior session has a legacy
			// preview-shifts/{n}.mp3 from before this feature shipped. After
			// preview-stems runs in the new session, the stem WAV exists. The
			// handler MUST recompute on the stem; serving the stale legacy file
			// would put vocals back into the audio (the chipmunk bug).
			name: "stem WAV present + legacy cache present — must compute on stem, ignore legacy",
			body: shiftBody(validID, validSig, -5),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write BOTH: stem WAV (new) and legacy shifted (stale chipmunk).
				stemP, _ := st.LocalPath(context.Background(), validID, "preview-stems/no_vocals.wav")
				_ = os.MkdirAll(filepath.Dir(stemP), 0o755)
				_ = os.WriteFile(stemP, []byte("stem wav bytes"), 0o644)
				legacyP, _ := st.LocalPath(context.Background(), validID, "preview-shifts/-5.mp3")
				_ = os.MkdirAll(filepath.Dir(legacyP), 0o755)
				_ = os.WriteFile(legacyP, []byte("STALE LEGACY CHIPMUNK"), 0o644)
				proc := &fakeProcessor{writeBytes: []byte("fresh clean stem shift")}
				return st, &fakeYouTubeShift{}, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "fresh clean stem shift",
			wantDownloadCalled: 0,
			wantShiftCalled:    1,
			wantShiftSemitones: -5.0,
			wantInputSuffix:    "preview-stems/no_vocals.wav",
			wantCached:         "preview-stems/shifted/-5.mp3",
			wantContentTypeAny: true,
		},
		{
			name: "both shifted caches absent, stem WAV present — shifts stem",
			body: shiftBody(validID, validSig, 4),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				// Pre-write the stem WAV only.
				p, _ := st.LocalPath(context.Background(), validID, "preview-stems/no_vocals.wav")
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, []byte("stem wav bytes"), 0o644)
				proc := &fakeProcessor{writeBytes: []byte("stem shifted output")}
				return st, &fakeYouTubeShift{}, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "stem shifted output",
			wantDownloadCalled: 0,
			wantShiftCalled:    1,
			wantShiftSemitones: 4.0,
			wantInputSuffix:    "preview-stems/no_vocals.wav",
			wantCached:         "preview-stems/shifted/4.mp3",
			wantContentTypeAny: true,
		},
		{
			name: "both shifted caches absent, stem WAV also absent — legacy full-mix fallback",
			body: shiftBody(validID, validSig, -5),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newRealStorage(t)
				proc := &fakeProcessor{writeBytes: []byte("legacy fallback output")}
				yt := &fakeYouTubeShift{
					onDownload: func(videoID string) {
						pp, _ := st.LocalPath(context.Background(), videoID, "preview.mp3")
						_ = os.MkdirAll(filepath.Dir(pp), 0o755)
						_ = os.WriteFile(pp, []byte("preview bytes"), 0o644)
					},
				}
				return st, yt, proc
			},
			wantStatus:         http.StatusOK,
			wantBody:           "legacy fallback output",
			wantDownloadCalled: 1,
			wantShiftCalled:    1,
			wantShiftSemitones: -5.0,
			wantInputSuffix:    "/preview.mp3",
			wantCached:         "preview-shifts/-5.mp3",
			wantContentTypeAny: true,
		},
		{
			name: "stem-shifted cache lookup error — 500",
			body: shiftBody(validID, validSig, 2),
			setup: func(t *testing.T) (services.Storage, *fakeYouTubeShift, *fakeProcessor) {
				st := newErrStorage(t, "preview-stems/shifted/2.mp3")
				return st, &fakeYouTubeShift{}, &fakeProcessor{}
			},
			wantStatus:         http.StatusInternalServerError,
			wantBodyContains:   "storage check failed",
			wantDownloadCalled: 0,
			wantShiftCalled:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, yt, proc := tt.setup(t)
			router := shiftRouter(signer, st, yt, proc)

			req := httptest.NewRequest(http.MethodPost, "/api/preview-shift", strings.NewReader(tt.body))
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

			if tt.wantBody != "" {
				if got := rec.Body.String(); got != tt.wantBody {
					t.Errorf("body: got %q, want %q", got, tt.wantBody)
				}
			}

			if got, want := yt.callCount, tt.wantDownloadCalled; got != want {
				t.Errorf("DownloadPreview call count: got %d, want %d", got, want)
			}

			if got, want := proc.callCount, tt.wantShiftCalled; got != want {
				t.Errorf("Shift call count: got %d, want %d", got, want)
			}

			if tt.wantShiftCalled > 0 {
				if got, want := proc.lastSemi, tt.wantShiftSemitones; got != want {
					t.Errorf("Shift semitones: got %v, want %v", got, want)
				}
				if tt.wantInputSuffix != "" && !strings.HasSuffix(proc.lastInput, tt.wantInputSuffix) {
					t.Errorf("Shift inputPath: got %q, want suffix %q", proc.lastInput, tt.wantInputSuffix)
				}
			}

			if tt.wantCached != "" {
				if realSt, ok := st.(*services.LocalDiskStorage); ok {
					ok2, err := realSt.Has(context.Background(), validID, tt.wantCached)
					if err != nil {
						t.Errorf("storage.Has after request: %v", err)
					} else if !ok2 {
						t.Errorf("storage.Has(%q): got false, want true — Commit did not run", tt.wantCached)
					}
				}
			}

			if tt.wantContentTypeAny && rec.Code == http.StatusOK {
				ct := rec.Header().Get("Content-Type")
				if !strings.HasPrefix(ct, "audio/") && !strings.HasPrefix(ct, "application/octet-stream") {
					t.Errorf("Content-Type: got %q, want audio/* or application/octet-stream", ct)
				}
			}
		})
	}
}

func TestPreviewShiftHandler_RangeRequest(t *testing.T) {
	const validID = "dQw4w9WgXcQ"

	signer := newShiftSigner(t)
	validSig := signer.Sign(validID)

	tests := []struct {
		name string
	}{
		{name: "range request returns 206"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newRealStorage(t)
			// Pre-cache the shifted file.
			shiftedPath, _ := st.LocalPath(context.Background(), validID, "preview-shifts/-2.mp3")
			_ = os.MkdirAll(filepath.Dir(shiftedPath), 0o755)
			_ = os.WriteFile(shiftedPath, []byte("hello world full"), 0o644)

			router := shiftRouter(signer, st, &fakeYouTubeShift{}, &fakeProcessor{})

			req := httptest.NewRequest(http.MethodPost, "/api/preview-shift", strings.NewReader(shiftBody(validID, validSig, -2)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Range", "bytes=0-4")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got := rec.Code; got != http.StatusPartialContent {
				t.Errorf("status: got %d, want %d (body: %s)", got, http.StatusPartialContent, rec.Body.String())
			}
			if got := len(rec.Body.Bytes()); got != 5 {
				t.Errorf("body length: got %d, want 5", got)
			}
		})
	}
}
