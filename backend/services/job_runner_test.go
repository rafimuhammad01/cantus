package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

type fakeProcessorJob struct {
	separateCalls int
	melodyCalls   int
	separateErr   error
	melodyErr     error
	separateFn    func(in, outDir string) error
	melodyFn      func(in, out string) error
	// blockSeparate, when non-nil, is received to unblock Separate (concurrency test).
	blockSeparate chan struct{}
}

func (f *fakeProcessorJob) Separate(_ context.Context, inputPath, outputDir string) (string, string, error) {
	f.separateCalls++
	if f.blockSeparate != nil {
		<-f.blockSeparate
	}
	if f.separateFn != nil {
		if err := f.separateFn(inputPath, outputDir); err != nil {
			return "", "", err
		}
	}
	if f.separateErr != nil {
		return "", "", f.separateErr
	}
	return filepath.Join(outputDir, "vocals.wav"), filepath.Join(outputDir, "no_vocals.wav"), nil
}
func (f *fakeProcessorJob) Melody(_ context.Context, vocalsPath, outputPath string) error {
	f.melodyCalls++
	if f.melodyFn != nil {
		if err := f.melodyFn(vocalsPath, outputPath); err != nil {
			return err
		}
	}
	return f.melodyErr
}
func (f *fakeProcessorJob) Shift(_ context.Context, inputPath, outputPath string, semitones float64) error {
	return nil
}

func (f *fakeProcessorJob) PreviewKey(_ context.Context, _ string) (string, error) { return "", nil }

type fakeCPUJob struct {
	shiftFn func(ctx context.Context, in, out string, st float64) error
	calls   []struct {
		In, Out string
		Semi    float64
	}
}

func (f *fakeCPUJob) Shift(ctx context.Context, in, out string, st float64) error {
	f.calls = append(f.calls, struct {
		In, Out string
		Semi    float64
	}{in, out, st})
	if f.shiftFn != nil {
		return f.shiftFn(ctx, in, out, st)
	}
	return nil
}
func (f *fakeCPUJob) PreviewKey(context.Context, string) (string, error) { return "", nil }

type fakeGPUJob struct{}

func (f *fakeGPUJob) Separate(context.Context, string, string, string) error { return nil }
func (f *fakeGPUJob) Melody(context.Context, string, string) error           { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeTestFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// newTestSetup creates a storage, jobStore, and runner all rooted at a temp dir.
// maxConcurrent controls the semaphore size.
func newTestSetup(t *testing.T, maxConcurrent int) (
	storage *services.LocalDiskStorage,
	jobStore *services.JobStore,
	fakeYT *fakeYouTubeJob,
	fakeProc *fakeProcessorJob,
	fakeCPU *fakeCPUJob,
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
	fakeProc = &fakeProcessorJob{}
	fakeCPU = &fakeCPUJob{}
	runner = services.NewJobRunner(fakeYT, storage, fakeCPU, &fakeGPUJob{}, fakeProc, jobStore, maxConcurrent)
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
		p := storage.FilesystemPathForLocalProcessor(storage.Key(videoID, name))
		if err := writeTestFile(p, "fake content"); err != nil {
			t.Fatalf("writeTestFile(%q): %v", p, err)
		}
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
				"original.wav",
				"vocals.wav",
				"no_vocals.wav",
				"melody.json",
				"shifted/-2/audio.mp3",
			},
			wantStatus:        models.StatusDone,
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:               "partial cache: original.wav exists, stems missing",
			preStage:           []string{"original.wav"},
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
			preStage:          []string{"original.wav", "vocals.wav", "no_vocals.wav"},
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
				"original.wav",
				"vocals.wav",
				"no_vocals.wav",
				"melody.json",
				"shifted/-2/audio.mp3",
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
			wantDownloadCalls: 1,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:              "separate fails",
			preStage:          []string{"original.wav"},
			separateErr:       errors.New("demucs: out of memory"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "separate",
			wantDownloadCalls: 0,
			wantSeparateCalls: 1,
			wantMelodyCalls:   0,
			wantShiftCalls:    0,
		},
		{
			name:              "melody fails",
			preStage:          []string{"original.wav", "vocals.wav", "no_vocals.wav"},
			melodyErr:         errors.New("crepe: model not loaded"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "melody",
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   1,
			wantShiftCalls:    0,
		},
		{
			name:              "shift fails",
			preStage:          []string{"original.wav", "vocals.wav", "no_vocals.wav", "melody.json"},
			shiftErr:          errors.New("rubberband: invalid input"),
			wantStatus:        models.StatusError,
			wantMsgContains:   "shift",
			wantDownloadCalls: 0,
			wantSeparateCalls: 0,
			wantMelodyCalls:   0,
			wantShiftCalls:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, jobStore, fakeYT, fakeProc, fakeCPU, runner := newTestSetup(t, 1)
			ctx := context.Background()

			// Pre-stage cache files.
			if len(tt.preStage) > 0 {
				stageFiles(t, storage, videoID, tt.preStage...)
			}

			// Wire up fake errors.
			fakeYT.downloadFullErr = tt.downloadFullErr
			fakeProc.separateErr = tt.separateErr
			fakeProc.melodyErr = tt.melodyErr

			// Wire up side-effect fns that write fake output files.
			if tt.writeSeparateFiles {
				fakeProc.separateFn = func(in, outDir string) error {
					if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "fake vocals"); err != nil {
						return err
					}
					return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "fake no_vocals")
				}
			}
			if tt.writeMelodyFile {
				fakeProc.melodyFn = func(in, out string) error {
					return writeTestFile(out, `{"hop_ms":10,"frames":[]}`)
				}
			}

			// Wire cpu Shift: either fail or commit a file into storage so Verify passes.
			shiftKey := storage.Key(videoID, "shifted/"+strconv.Itoa(semitones)+"/audio.mp3")
			if tt.shiftErr != nil {
				shiftErrVal := tt.shiftErr
				fakeCPU.shiftFn = func(_ context.Context, _, _ string, _ float64) error {
					return shiftErrVal
				}
			} else if tt.writeShiftFile {
				fakeCPU.shiftFn = func(_ context.Context, _, _ string, _ float64) error {
					commitFile(t, storage, shiftKey, "fake mp3 bytes")
					return nil
				}
			}

			// DownloadFull side effect: write original.wav so Separate stage can find it.
			if tt.downloadFullErr == nil && tt.writeSeparateFiles {
				fakeYT.downloadFullFn = func(vid string) error {
					p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
					return writeTestFile(p, "fake original wav")
				}
			}

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
			if fakeProc.separateCalls != tt.wantSeparateCalls {
				t.Errorf("separateCalls: got %d, want %d", fakeProc.separateCalls, tt.wantSeparateCalls)
			}
			if fakeProc.melodyCalls != tt.wantMelodyCalls {
				t.Errorf("melodyCalls: got %d, want %d", fakeProc.melodyCalls, tt.wantMelodyCalls)
			}
			if got := len(fakeCPU.calls); got != tt.wantShiftCalls {
				t.Errorf("shiftCalls: got %d, want %d", got, tt.wantShiftCalls)
			}

			// On success, verify shifted file is in cache.
			if tt.wantStatus == models.StatusDone && tt.writeShiftFile {
				has, err := storage.Has(ctx, storage.Key(videoID, "shifted/-2/audio.mp3"))
				if err != nil {
					t.Fatalf("storage.Has(shifted/-2/audio.mp3): %v", err)
				}
				if !has {
					t.Error("shifted/-2/audio.mp3 not found in cache after successful run")
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
	storage, jobStore, fakeYT, fakeProc, fakeCPU, runner := newTestSetup(t, 1)
	const videoID = "dQw4w9WgXcQ"

	// Wire side-effect fns so pipeline can complete.
	fakeYT.downloadFullFn = func(vid string) error {
		p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
		return writeTestFile(p, "fake original")
	}
	fakeProc.separateFn = func(in, outDir string) error {
		if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "v"); err != nil {
			return err
		}
		return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "nv")
	}
	fakeProc.melodyFn = func(in, out string) error {
		return writeTestFile(out, `{}`)
	}
	fakeCPU.shiftFn = func(_ context.Context, _, _ string, semi float64) error {
		key := storage.Key(videoID, "shifted/"+strconv.FormatFloat(semi, 'f', -1, 64)+"/audio.mp3")
		commitFile(t, storage, key, "mp3")
		return nil
	}

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
	if fakeProc.separateCalls != 1 {
		t.Errorf("separateCalls: got %d, want 1", fakeProc.separateCalls)
	}
	if fakeProc.melodyCalls != 1 {
		t.Errorf("melodyCalls: got %d, want 1", fakeProc.melodyCalls)
	}
	if got := len(fakeCPU.calls); got != 1 {
		t.Errorf("shiftCalls: got %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// TestJobRunner_Submit_Dedup — videoID-level deduplication tests
// ---------------------------------------------------------------------------

func TestJobRunner_Submit_Dedup(t *testing.T) {
	// ---------------------------------------------------------------------------
	// Case 1: concurrent submits for the same videoID return the same jobID and
	// only one pipeline run (Separate called once).
	// ---------------------------------------------------------------------------
	t.Run("concurrent same videoID returns same jobID", func(t *testing.T) {
		root := t.TempDir()
		storage, err := services.NewLocalDiskStorage(root)
		if err != nil {
			t.Fatalf("NewLocalDiskStorage: %v", err)
		}
		jobStore := services.NewJobStore(time.Hour)
		fakeYT := &fakeYouTubeJob{}

		// ready is closed by separateFn on first entry, signalling that the first
		// goroutine has reached Separate and is about to block.
		// blockSeparate is closed by the test to release the first goroutine.
		ready := make(chan struct{})
		blockSeparate := make(chan struct{})
		fakeProc := &fakeProcessorJob{}
		fakeCPU := &fakeCPUJob{}

		runner := services.NewJobRunner(fakeYT, storage, fakeCPU, &fakeGPUJob{}, fakeProc, jobStore, 2) // allow 2 concurrent so semaphore is not the bottleneck

		const videoID = "dedupvideo1"

		fakeYT.downloadFullFn = func(vid string) error {
			p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
			return writeTestFile(p, "orig")
		}
		fakeProc.separateFn = func(in, outDir string) error {
			// Signal that we have entered Separate, then block until the test releases us.
			close(ready)
			<-blockSeparate
			if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "v"); err != nil {
				return err
			}
			return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "nv")
		}
		fakeProc.melodyFn = func(in, out string) error {
			return writeTestFile(out, `{}`)
		}
		fakeCPU.shiftFn = func(_ context.Context, _, _ string, semi float64) error {
			key := storage.Key(videoID, "shifted/"+strconv.FormatFloat(semi, 'f', -1, 64)+"/audio.mp3")
			commitFile(t, storage, key, "mp3")
			return nil
		}

		// First submit — this goroutine will block in separateFn waiting for blockSeparate.
		jobID1 := runner.Submit(videoID, 0)

		// Wait until the first goroutine has entered Separate (deterministic, no sleep).
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("first Submit never reached Separate")
		}

		// Second submit for the same videoID (different semitones) — must return same jobID.
		jobID2 := runner.Submit(videoID, 3)

		if jobID1 != jobID2 {
			t.Errorf("expected dedup: jobID1=%q, jobID2=%q should be equal", jobID1, jobID2)
		}

		// Unblock the first pipeline.
		close(blockSeparate)

		// Wait for completion.
		waitForStatus(t, jobStore, jobID1, models.StatusDone, 3*time.Second)

		// Only ONE Separate call should have happened.
		if fakeProc.separateCalls != 1 {
			t.Errorf("separateCalls: got %d, want 1 (dedup should prevent second pipeline)", fakeProc.separateCalls)
		}
	})

	// ---------------------------------------------------------------------------
	// Case 2: after a job completes, the inflight entry is released so a follow-up
	// Submit starts a fresh job with a new jobID.
	// ---------------------------------------------------------------------------
	t.Run("post-completion submit returns new jobID", func(t *testing.T) {
		storage, jobStore, fakeYT, fakeProc, fakeCPU, runner := newTestSetup(t, 1)
		const videoID = "dedupvideo2"

		fakeYT.downloadFullFn = func(vid string) error {
			p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
			return writeTestFile(p, "orig")
		}
		fakeProc.separateFn = func(in, outDir string) error {
			if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "v"); err != nil {
				return err
			}
			return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "nv")
		}
		fakeProc.melodyFn = func(in, out string) error {
			return writeTestFile(out, `{}`)
		}
		fakeCPU.shiftFn = func(_ context.Context, _, _ string, semi float64) error {
			key := storage.Key(videoID, "shifted/"+strconv.FormatFloat(semi, 'f', -1, 64)+"/audio.mp3")
			commitFile(t, storage, key, "mp3")
			return nil
		}

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
		storage, jobStore, fakeYT, fakeProc, fakeCPU, runner := newTestSetup(t, 1)
		const videoID = "dedupvideo3"

		// Wire download to succeed (writes original.wav) so we reach Separate.
		fakeYT.downloadFullFn = func(vid string) error {
			p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
			return writeTestFile(p, "orig")
		}
		// Separate returns an error to fail the pipeline.
		fakeProc.separateErr = errors.New("demucs: GPU OOM")

		jobID1 := runner.Submit(videoID, 0)
		waitForStatus(t, jobStore, jobID1, models.StatusError, 3*time.Second)

		// Reset error so second run can succeed.
		fakeProc.separateErr = nil
		fakeProc.separateFn = func(in, outDir string) error {
			if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "v"); err != nil {
				return err
			}
			return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "nv")
		}
		fakeProc.melodyFn = func(in, out string) error {
			return writeTestFile(out, `{}`)
		}
		fakeCPU.shiftFn = func(_ context.Context, _, _ string, semi float64) error {
			key := storage.Key(videoID, "shifted/"+strconv.FormatFloat(semi, 'f', -1, 64)+"/audio.mp3")
			commitFile(t, storage, key, "mp3")
			return nil
		}

		jobID2 := runner.Submit(videoID, 0)

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
	// wire a custom blocking processor before the runner is created.
	root := t.TempDir()
	storage, err := services.NewLocalDiskStorage(root)
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}

	jobStore := services.NewJobStore(time.Hour)
	fakeYT := &fakeYouTubeJob{}

	// blockCh gates the Separate call — first receive blocks until close().
	blockCh := make(chan struct{})
	fakeProc := &fakeProcessorJob{blockSeparate: blockCh}
	fakeCPU := &fakeCPUJob{}

	runner := services.NewJobRunner(fakeYT, storage, fakeCPU, &fakeGPUJob{}, fakeProc, jobStore, 1)

	const vid1 = "aaaaaaaaaaa"
	const vid2 = "bbbbbbbbbbb"

	// Both jobs share fakeYT; downloadFullFn writes original.wav per videoID.
	fakeYT.downloadFullFn = func(vid string) error {
		p := storage.FilesystemPathForLocalProcessor(storage.Key(vid, "original.wav"))
		return writeTestFile(p, "orig")
	}
	fakeProc.separateFn = func(in, outDir string) error {
		if err := writeTestFile(filepath.Join(outDir, "vocals.wav"), "v"); err != nil {
			return err
		}
		return writeTestFile(filepath.Join(outDir, "no_vocals.wav"), "nv")
	}
	fakeProc.melodyFn = func(in, out string) error {
		return writeTestFile(out, `{}`)
	}
	fakeCPU.shiftFn = func(_ context.Context, _, _ string, semi float64) error {
		// Can't predict which vid runs first so use a temp key based on the URL.
		// Instead commit the shifted file for both possible video IDs.
		for _, vid := range []string{vid1, vid2} {
			key := storage.Key(vid, "shifted/"+strconv.FormatFloat(semi, 'f', -1, 64)+"/audio.mp3")
			src := filepath.Join(t.TempDir(), "shifted.mp3")
			_ = os.WriteFile(src, []byte("mp3"), 0o644)
			_ = storage.Commit(context.Background(), key, src)
		}
		return nil
	}

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
