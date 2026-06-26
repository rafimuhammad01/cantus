package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"cantus/backend/models"
	"cantus/backend/services"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeYouTubeJob struct {
	downloadFullCalls int
	downloadFullErr   error
	downloadFullFn    func(videoID string) error
}

func (f *fakeYouTubeJob) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}
func (f *fakeYouTubeJob) DownloadPreview(_ context.Context, _ string) error { return nil }
func (f *fakeYouTubeJob) DownloadFull(_ context.Context, videoID string) error {
	f.downloadFullCalls++
	if f.downloadFullFn != nil {
		if err := f.downloadFullFn(videoID); err != nil {
			return err
		}
	}
	return f.downloadFullErr
}

type fakeShifter struct {
	calls []struct {
		In, Out string
		Semi    float64
	}
	shiftFn func(ctx context.Context, in, out string, st float64) error
}

func (f *fakeShifter) Shift(ctx context.Context, in, out string, st float64) error {
	f.calls = append(f.calls, struct {
		In, Out string
		Semi    float64
	}{in, out, st})
	if f.shiftFn != nil {
		return f.shiftFn(ctx, in, out, st)
	}
	// Default: write a non-empty file at out so storage.Commit downstream finds bytes.
	return os.WriteFile(out, []byte("shifted"), 0o644)
}

type fakeProcessorJob struct {
	separateFn  func(ctx context.Context, inURL, vocalsURL, noVocalsURL string) error
	melodyFn    func(ctx context.Context, vocalsURL, outURL string) error
	melodyCalls int
}

func (f *fakeProcessorJob) Separate(ctx context.Context, inURL, vocalsURL, noVocalsURL string) error {
	if f.separateFn != nil {
		return f.separateFn(ctx, inURL, vocalsURL, noVocalsURL)
	}
	return nil
}
func (f *fakeProcessorJob) Melody(ctx context.Context, vocalsURL, outURL string) error {
	f.melodyCalls++
	if f.melodyFn != nil {
		return f.melodyFn(ctx, vocalsURL, outURL)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestSetup creates a storage, jobStore, and runner all rooted at a temp dir.
// maxConcurrent controls the semaphore size.
func newTestSetup(t *testing.T, maxConcurrent int) (
	storage *services.LocalDiskStorage,
	jobStore *services.JobStore,
	fakeYT *fakeYouTubeJob,
	fakeShift *fakeShifter,
	runner *services.JobRunner,
) {
	t.Helper()
	root := t.TempDir()
	var err error
	storage, err = services.NewLocalDiskStorage(root)
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}
	jobStore = services.NewJobStore(time.Hour)
	fakeYT = &fakeYouTubeJob{}
	fakeShift = &fakeShifter{}
	// fakeProc is wired inside each test via runner replacement or via the default no-op.
	// newTestSetup always creates a no-op processor fake; tests that need separation side effects
	// must set up a custom runner.
	runner = services.NewJobRunner(fakeYT, storage, &fakeProcessorJob{}, fakeShift, jobStore, maxConcurrent)
	return
}

// waitForStatus polls JobStore.Get until the job reaches wantStatus or timeout elapses.
func waitForStatus(t *testing.T, store *services.JobStore, jobID string, wantStatus models.JobStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		j, ok := store.Get(jobID)
		if ok && j.Status == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := store.Get(jobID)
	t.Fatalf("timed out waiting for status %q; got %q (message: %q)", wantStatus, j.Status, j.Message)
}

// stageFiles is a helper to pre-stage a set of named cache files for a videoID.
func stageFiles(t *testing.T, storage *services.LocalDiskStorage, videoID string, names ...string) {
	t.Helper()
	for _, name := range names {
		commitFile(t, storage, storage.Key(videoID, name), "fake content")
	}
}

// commitFile writes content to a temp file and commits it into storage under the given key.
func commitFile(t *testing.T, storage *services.LocalDiskStorage, key, content string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "tmp")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := storage.Commit(context.Background(), key, src); err != nil {
		t.Fatalf("storage.Commit(%q): %v", key, err)
	}
}

// ---------------------------------------------------------------------------
// TestJobRunner_Run — table-driven, synchronous via runner.Run(...)
// ---------------------------------------------------------------------------

func TestJobRunner_Run(t *testing.T) {
	const videoID = "dQw4w9WgXcQ"
	const semitones = -2

	tests := []struct {
		name            string
		preStage        []string // file names to pre-stage in cache
		downloadFullErr error
		separateErr     error
		melodyErr       error
		shiftErr        error
		// side-effect fns to write fake files so next stage can proceed
		writeSeparateFiles bool
		writeMelodyFile    bool
		writeShiftFile     bool
		wantStatus         models.JobStatus
		wantMsgContains    string // non-empty: error message must contain this
		wantDownloadCalls  int
		wantSeparateCalls  int
		wantMelodyCalls    int
		wantShiftCalls     int
	}{
		{
			name:               "happy path cold - nothing cached",
			preStage:           nil,
			writeSeparateFiles: true,
			writeMelodyFile:    true,
			writeShiftFile:     true,
			wantStatus:         models.StatusDone,
			wantDownloadCalls:  1,
			wantSeparateCalls:  1,
			wantMelodyCalls:    1,
			wantShiftCalls:     1,
		},
		{
			name: "happy path warm - everything cached",
			preStage: []string{
				"original" + services.AudioExt,
				"vocals" + services.AudioExt,
				"no_vocals" + services.AudioExt,
				"melody.json",
				"shifted/-2/audio" + services.AudioExt,
			},
			wantStatus:        models.StatusDone,
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:               "partial cache: original.mp3 exists, stems missing",
			preStage:           []string{"original" + services.AudioExt},
			writeSeparateFiles: true,
			writeMelodyFile:    true,
			writeShiftFile:     true,
			wantStatus:         models.StatusDone,
			wantDownloadCalls:  0,
			wantSeparateCalls:  1,
			wantMelodyCalls:    1,
			wantShiftCalls:     1,
		},
		{
			name:              "partial cache: stems exist, melody.json missing",
			preStage:          []string{"original" + services.AudioExt, "vocals" + services.AudioExt, "no_vocals" + services.AudioExt},
			writeMelodyFile:   true,
			writeShiftFile:    true,
			wantStatus:        models.StatusDone,
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   1,
			wantShiftCalls:    1,
		},
		{
			name: "partial cache: shifted file already exists",
			preStage: []string{
				"original" + services.AudioExt,
				"vocals" + services.AudioExt,
				"no_vocals" + services.AudioExt,
				"melody.json",
				"shifted/-2/audio" + services.AudioExt,
			},
			wantStatus:        models.StatusDone,
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:              "download fails",
			preStage:          nil,
			downloadFullErr:   errors.New("yt-dlp: network error"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "download",
			wantDownloadCalls: services.PipelineRetryAttempts,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:              "separate fails",
			preStage:          []string{"original" + services.AudioExt},
			separateErr:       errors.New("demucs: out of memory"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "separate",
			wantDownloadCalls: 0,
			wantSeparateCalls: services.PipelineRetryAttempts,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:              "melody fails",
			preStage:          []string{"original" + services.AudioExt, "vocals" + services.AudioExt, "no_vocals" + services.AudioExt},
			melodyErr:         errors.New("crepe: model not loaded"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "melody",
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   services.PipelineRetryAttempts,
			wantShiftCalls:    0,
		},
		{
			name:              "shift fails",
			preStage:          []string{"original" + services.AudioExt, "vocals" + services.AudioExt, "no_vocals" + services.AudioExt, "melody.json"},
			shiftErr:          errors.New("rubberband: invalid input"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "shift",
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    services.PipelineRetryAttempts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, jobStore, fakeYT, fakeShift, _ := newTestSetup(t, 1)
			ctx := context.Background()

			// Per-test fakeProcessorJob so we can configure separateFn/melodyFn and track call counts.
			var gpuSeparateCalls int
			fakeProc := &fakeProcessorJob{}

			// Pre-stage cache files.
			if len(tt.preStage) > 0 {
				stageFiles(t, storage, videoID, tt.preStage...)
			}

			// Wire up fake errors.
			fakeYT.downloadFullErr = tt.downloadFullErr

			// Wire GPU Separate: commit stems into storage so Verify passes, or return error.
			if tt.separateErr != nil {
				sepErrVal := tt.separateErr
				fakeProc.separateFn = func(_ context.Context, _, _, _ string) error {
					gpuSeparateCalls++
					return sepErrVal
				}
			} else if tt.writeSeparateFiles {
				fakeProc.separateFn = func(_ context.Context, _, _, _ string) error {
					gpuSeparateCalls++
					commitFile(t, storage, storage.Key(videoID, "vocals"+services.AudioExt), "fake vocals")
					commitFile(t, storage, storage.Key(videoID, "no_vocals"+services.AudioExt), "fake no_vocals")
					return nil
				}
			}

			// Wire GPU Melody: commit melody.json into storage so Verify passes, or return error.
			if tt.melodyErr != nil {
				melodyErrVal := tt.melodyErr
				fakeProc.melodyFn = func(_ context.Context, _, _ string) error {
					return melodyErrVal
				}
			} else if tt.writeMelodyFile {
				fakeProc.melodyFn = func(_ context.Context, _, _ string) error {
					commitFile(t, storage, storage.Key(videoID, "melody.json"), `{"hop_ms":10,"frames":[]}`)
					return nil
				}
			}

			// Wire shifter: either fail or write bytes to out so Commit downstream finds a non-empty file.
			if tt.shiftErr != nil {
				shiftErrVal := tt.shiftErr
				fakeShift.shiftFn = func(_ context.Context, _, _ string, _ float64) error {
					return shiftErrVal
				}
			} else if tt.writeShiftFile {
				fakeShift.shiftFn = func(_ context.Context, _, out string, _ float64) error {
					return os.WriteFile(out, []byte("fake mp3 bytes"), 0o644)
				}
			}

			// DownloadFull side effect: write original.wav so Separate stage can find it.
			if tt.downloadFullErr == nil && tt.writeSeparateFiles {
				fakeYT.downloadFullFn = func(vid string) error {
					commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "fake original wav")
					return nil
				}
			}

			// Build a fresh runner with the per-test fakeProc.
			runner := services.NewJobRunner(fakeYT, storage, fakeProc, fakeShift, jobStore, 1)

			// Create a job so jobStore has the record.
			jobID := "test-job-run"
			jobStore.Create(models.Job{ID: jobID, Status: models.StatusQueued, CreatedAt: time.Now()})

			// Run synchronously.
			runner.Run(ctx, jobID, videoID, semitones)

			// Verify final status.
			job, ok := jobStore.Get(jobID)
			if !ok {
				t.Fatal("job not found in store")
			}
			if job.Status != tt.wantStatus {
				t.Errorf("status: got %q, want %q (message: %q)", job.Status, tt.wantStatus, job.Message)
			}
			if tt.wantMsgContains != "" && !containsStr(job.Message, tt.wantMsgContains) {
				t.Errorf("message: got %q, want it to contain %q", job.Message, tt.wantMsgContains)
			}

			// Verify call counts.
			if fakeYT.downloadFullCalls != tt.wantDownloadCalls {
				t.Errorf("downloadFullCalls: got %d, want %d", fakeYT.downloadFullCalls, tt.wantDownloadCalls)
			}
			if gpuSeparateCalls != tt.wantSeparateCalls {
				t.Errorf("separateCalls: got %d, want %d", gpuSeparateCalls, tt.wantSeparateCalls)
			}
			if fakeProc.melodyCalls != tt.wantMelodyCalls {
				t.Errorf("melodyCalls: got %d, want %d", fakeProc.melodyCalls, tt.wantMelodyCalls)
			}
			if got := len(fakeShift.calls); got != tt.wantShiftCalls {
				t.Errorf("shiftCalls: got %d, want %d", got, tt.wantShiftCalls)
			}

			// On success, verify shifted file is in cache.
			if tt.wantStatus == models.StatusDone && tt.writeShiftFile {
				has, err := storage.Has(ctx, storage.Key(videoID, "shifted/-2/audio"+services.AudioExt))
				if err != nil {
					t.Fatalf("storage.Has(shifted/-2/audio.wav): %v", err)
				}
				if !has {
					t.Error("shifted/-2/audio.wav not found in cache after successful run")
				}
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}()
}

// ---------------------------------------------------------------------------
// TestJobRunner_Submit_RunsAsync
// ---------------------------------------------------------------------------

func TestJobRunner_Submit_RunsAsync(t *testing.T) {
	storage, jobStore, fakeYT, fakeShift, _ := newTestSetup(t, 1)
	const videoID = "dQw4w9WgXcQ"

	var gpuSeparateCalls int
	fakeProc := &fakeProcessorJob{
		separateFn: func(_ context.Context, _, _, _ string) error {
			gpuSeparateCalls++
			commitFile(t, storage, storage.Key(videoID, "vocals"+services.AudioExt), "v")
			commitFile(t, storage, storage.Key(videoID, "no_vocals"+services.AudioExt), "nv")
			return nil
		},
		melodyFn: func(_ context.Context, _, _ string) error {
			commitFile(t, storage, storage.Key(videoID, "melody.json"), `{}`)
			return nil
		},
	}
	runner := services.NewJobRunner(fakeYT, storage, fakeProc, fakeShift, jobStore, 1)

	// Wire side-effect fns so pipeline can complete.
	fakeYT.downloadFullFn = func(vid string) error {
		commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "fake original")
		return nil
	}
	// fakeShifter default fn writes "shifted" bytes to out path — sufficient for Commit+Verify.

	jobID := runner.Submit(videoID, -2)

	if jobID == "" {
		t.Fatal("Submit returned empty jobID")
	}

	// Job must exist immediately.
	job, ok := jobStore.Get(jobID)
	if !ok {
		t.Fatal("job not found in store immediately after Submit")
	}
	if job.Status != models.StatusQueued {
		t.Errorf("initial status: got %q, want %q", job.Status, models.StatusQueued)
	}

	// Eventually reaches Done.
	waitForStatus(t, jobStore, jobID, models.StatusDone, 2*time.Second)

	// Fake calls happened.
	if fakeYT.downloadFullCalls != 1 {
		t.Errorf("downloadFullCalls: got %d, want 1", fakeYT.downloadFullCalls)
	}
	if gpuSeparateCalls != 1 {
		t.Errorf("separateCalls: got %d, want 1", gpuSeparateCalls)
	}
	if fakeProc.melodyCalls != 1 {
		t.Errorf("melodyCalls: got %d, want 1", fakeProc.melodyCalls)
	}
	if got := len(fakeShift.calls); got != 1 {
		t.Errorf("shiftCalls: got %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// TestJobRunner_Submit_Dedup — videoID-level deduplication tests
// ---------------------------------------------------------------------------

func TestJobRunner_Submit_Dedup(t *testing.T) {
	// ---------------------------------------------------------------------------
	// Case 1: concurrent submits for the same (videoID, semitones) pair return
	// the same jobID. Different semitones for the same videoID start independent
	// jobs but share the cached stems — Separate runs at most once.
	// ---------------------------------------------------------------------------
	t.Run("concurrent same key returns same jobID; different semitones spawns separate job", func(t *testing.T) {
		root := t.TempDir()
		storage, err := services.NewLocalDiskStorage(root)
		if err != nil {
			t.Fatalf("NewLocalDiskStorage: %v", err)
		}
		jobStore := services.NewJobStore(time.Hour)
		fakeYT := &fakeYouTubeJob{}

		fakeShiftDedup1 := &fakeShifter{}

		var gpuSeparateCalls int
		var separateMu sync.Mutex
		fakeProc := &fakeProcessorJob{
			separateFn: func(_ context.Context, _, _, _ string) error {
				separateMu.Lock()
				gpuSeparateCalls++
				separateMu.Unlock()
				commitFile(t, storage, storage.Key("dedupvideo1", "vocals"+services.AudioExt), "v")
				commitFile(t, storage, storage.Key("dedupvideo1", "no_vocals"+services.AudioExt), "nv")
				return nil
			},
		}

		// Semaphore=1 → jobs serialize. Second job sees stems already committed
		// by the first job and skips Separate via storage.Has().
		runner := services.NewJobRunner(fakeYT, storage, fakeProc, fakeShiftDedup1, jobStore, 1)

		const videoID = "dedupvideo1"

		fakeYT.downloadFullFn = func(vid string) error {
			commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "orig")
			return nil
		}
		fakeProc.melodyFn = func(_ context.Context, _, _ string) error {
			commitFile(t, storage, storage.Key(videoID, "melody.json"), `{}`)
			return nil
		}

		// Two submits for the SAME (videoID, semitones=0) — must dedup.
		jobID1a := runner.Submit(videoID, 0)
		jobID1b := runner.Submit(videoID, 0)
		if jobID1a != jobID1b {
			t.Errorf("identical (videoID, semitones) dedup: jobID1a=%q, jobID1b=%q should be equal", jobID1a, jobID1b)
		}

		// Submit for the SAME videoID but DIFFERENT semitones — must spawn a new job.
		jobID2 := runner.Submit(videoID, 3)
		if jobID1a == jobID2 {
			t.Errorf("different semitones should produce different jobIDs, but got same: %q", jobID1a)
		}

		// Wait for both to complete.
		waitForStatus(t, jobStore, jobID1a, models.StatusDone, 3*time.Second)
		waitForStatus(t, jobStore, jobID2, models.StatusDone, 3*time.Second)

		// Only ONE Separate call total — the second job hits the stem cache.
		separateMu.Lock()
		got := gpuSeparateCalls
		separateMu.Unlock()
		if got != 1 {
			t.Errorf("separateCalls: got %d, want 1 (stem cache should prevent second Separate)", got)
		}
	})

	// ---------------------------------------------------------------------------
	// Case 2: after a job completes, the inflight entry is released so a follow-up
	// Submit starts a fresh job with a new jobID.
	// ---------------------------------------------------------------------------
	t.Run("post-completion submit returns new jobID", func(t *testing.T) {
		storage, jobStore, fakeYT, fakeShift2, _ := newTestSetup(t, 1)
		const videoID = "dedupvideo2"

		fakeProc := &fakeProcessorJob{
			separateFn: func(_ context.Context, _, _, _ string) error {
				commitFile(t, storage, storage.Key(videoID, "vocals"+services.AudioExt), "v")
				commitFile(t, storage, storage.Key(videoID, "no_vocals"+services.AudioExt), "nv")
				return nil
			},
			melodyFn: func(_ context.Context, _, _ string) error {
				commitFile(t, storage, storage.Key(videoID, "melody.json"), `{}`)
				return nil
			},
		}
		runner := services.NewJobRunner(fakeYT, storage, fakeProc, fakeShift2, jobStore, 1)

		fakeYT.downloadFullFn = func(vid string) error {
			commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "orig")
			return nil
		}
		// fakeShift2 default fn writes bytes to out path — sufficient for Commit+Verify.

		jobID1 := runner.Submit(videoID, 0)
		waitForStatus(t, jobStore, jobID1, models.StatusDone, 3*time.Second)

		// Submit again after completion — must be a new job.
		jobID2 := runner.Submit(videoID, 0)

		if jobID1 == jobID2 {
			t.Errorf("expected fresh jobID after completion, but got same jobID: %q", jobID1)
		}
		if jobID2 == "" {
			t.Error("second Submit returned empty jobID")
		}

		waitForStatus(t, jobStore, jobID2, models.StatusDone, 3*time.Second)
	})

	// ---------------------------------------------------------------------------
	// Case 3: after a job's pipeline FAILS, the inflight entry is released so a
	// follow-up Submit for the same videoID starts a new job.
	// ---------------------------------------------------------------------------
	t.Run("post-failure submit returns new jobID", func(t *testing.T) {
		storage, jobStore, fakeYT, fakeShift3, _ := newTestSetup(t, 1)
		const videoID = "dedupvideo3"

		// First run: GPU Separate fails.
		failProc := &fakeProcessorJob{
			separateFn: func(_ context.Context, _, _, _ string) error {
				return errors.New("demucs: GPU OOM")
			},
		}
		runner := services.NewJobRunner(fakeYT, storage, failProc, fakeShift3, jobStore, 1)

		// Wire download to succeed (writes original.wav) so we reach Separate.
		fakeYT.downloadFullFn = func(vid string) error {
			commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "orig")
			return nil
		}

		jobID1 := runner.Submit(videoID, 0)
		// Retry loop: 3 attempts × (2s + 4s backoff) = ~6s minimum; allow extra headroom.
		waitForStatus(t, jobStore, jobID1, models.StatusError, 15*time.Second)

		// Second run: GPU Separate succeeds and commits stems.
		successProc := &fakeProcessorJob{
			separateFn: func(_ context.Context, _, _, _ string) error {
				commitFile(t, storage, storage.Key(videoID, "vocals"+services.AudioExt), "v")
				commitFile(t, storage, storage.Key(videoID, "no_vocals"+services.AudioExt), "nv")
				return nil
			},
		}
		runner2 := services.NewJobRunner(fakeYT, storage, successProc, fakeShift3, jobStore, 1)

		successProc.melodyFn = func(_ context.Context, _, _ string) error {
			commitFile(t, storage, storage.Key(videoID, "melody.json"), `{}`)
			return nil
		}
		// fakeShift3 default fn writes bytes to out path — sufficient for Commit+Verify.

		jobID2 := runner2.Submit(videoID, 0)

		if jobID1 == jobID2 {
			t.Errorf("expected new jobID after failure, but got same jobID: %q", jobID1)
		}
		if jobID2 == "" {
			t.Error("second Submit returned empty jobID")
		}

		waitForStatus(t, jobStore, jobID2, models.StatusDone, 3*time.Second)
	})
}

// ---------------------------------------------------------------------------
// TestJobRunner_Submit_BoundedConcurrency
// ---------------------------------------------------------------------------

func TestJobRunner_Submit_BoundedConcurrency(t *testing.T) {
	// Build storage manually; do not use newTestSetup here because we need to
	// wire a custom blocking GPU before the runner is created.
	root := t.TempDir()
	storage, err := services.NewLocalDiskStorage(root)
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}

	jobStore := services.NewJobStore(time.Hour)
	fakeYT := &fakeYouTubeJob{}

	// blockCh gates the GPU Separate call — first receive blocks until close().
	blockCh := make(chan struct{})
	fakeShiftConc := &fakeShifter{}

	const vid1 = "aaaaaaaaaaa"
	const vid2 = "bbbbbbbbbbb"

	fakeProc := &fakeProcessorJob{
		separateFn: func(_ context.Context, _, _, _ string) error {
			// Block until the test closes blockCh, simulating a long Demucs run.
			<-blockCh
			// We don't know which vid ran; commit stems for both so the pipeline can continue.
			for _, vid := range []string{vid1, vid2} {
				commitFile(t, storage, storage.Key(vid, "vocals"+services.AudioExt), "v")
				commitFile(t, storage, storage.Key(vid, "no_vocals"+services.AudioExt), "nv")
			}
			return nil
		},
		melodyFn: func(_ context.Context, _, _ string) error {
			// We don't know which vid is running; commit melody.json for both.
			for _, vid := range []string{vid1, vid2} {
				commitFile(t, storage, storage.Key(vid, "melody.json"), `{}`)
			}
			return nil
		},
	}

	runner := services.NewJobRunner(fakeYT, storage, fakeProc, fakeShiftConc, jobStore, 1)

	// Both jobs share fakeYT; downloadFullFn writes original.wav per videoID.
	fakeYT.downloadFullFn = func(vid string) error {
		commitFile(t, storage, storage.Key(vid, "original"+services.AudioExt), "orig")
		return nil
	}
	// fakeShiftConc default fn writes bytes to out path — sufficient for Commit+Verify.

	// Submit both jobs; capture both IDs as a set so we can check whichever runs first.
	jobID1 := runner.Submit(vid1, 0)
	jobID2 := runner.Submit(vid2, 0)
	allIDs := [2]string{jobID1, jobID2}

	// Wait until EITHER job reaches Separating — it holds the only semaphore slot.
	// We cannot assume which goroutine the scheduler picks first, so we poll both.
	var runningID, waitingID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		for _, id := range allIDs {
			j, _ := jobStore.Get(id)
			if j.Status == models.StatusSeparating {
				runningID = id
				if id == jobID1 {
					waitingID = jobID2
				} else {
					waitingID = jobID1
				}
				break
			}
		}
		if runningID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runningID == "" {
		j1, _ := jobStore.Get(jobID1)
		j2, _ := jobStore.Get(jobID2)
		t.Fatalf("timed out: neither job reached separating (job1=%q, job2=%q)", j1.Status, j2.Status)
	}

	// The waiting job must still be Queued (blocked on semaphore).
	waiting, ok := jobStore.Get(waitingID)
	if !ok {
		t.Fatal("waiting job not found in store")
	}
	if waiting.Status != models.StatusQueued {
		t.Errorf("waiting job status: got %q, want %q (semaphore should serialize)", waiting.Status, models.StatusQueued)
	}

	// Unblock: close the channel so the running job's Separate proceeds.
	close(blockCh)

	// Both jobs must eventually reach Done.
	waitForStatus(t, jobStore, jobID1, models.StatusDone, 3*time.Second)
	waitForStatus(t, jobStore, jobID2, models.StatusDone, 3*time.Second)
}
