package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"cantus/backend/models"
	zlog "github.com/rs/zerolog/log"
)

const (
	PipelineRetryAttempts  = 3
	PipelineRetryBaseDelay = 2 * time.Second
	// ShiftStageTimeout caps the entire stage-4 (rubberband + ffmpeg) phase per job.
	// Without this, a hung shifter call (e.g. libsndfile MP3 codec stall) would
	// keep the inflight entry forever and every retry would hit the same dead job.
	ShiftStageTimeout   = 3 * time.Minute
	maxFailuresPerVideo = 3
	failureCooldown     = 30 * time.Minute
)

// failureRecord tracks consecutive failures for a single videoID.
type failureRecord struct {
	count      int
	lastFailAt time.Time
}

// VideoFailureTracker tracks per-videoID failure counts and enforces a cooldown
// after maxFailuresPerVideo consecutive failures. Safe for concurrent use.
type VideoFailureTracker struct {
	mu      sync.Mutex
	records map[string]*failureRecord
}

// NewVideoFailureTracker returns an initialised VideoFailureTracker.
func NewVideoFailureTracker() *VideoFailureTracker {
	return &VideoFailureTracker{records: make(map[string]*failureRecord)}
}

// RecordFailure increments the failure count for videoID and notes the time.
func (t *VideoFailureTracker) RecordFailure(videoID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.records[videoID]
	if !ok {
		r = &failureRecord{}
		t.records[videoID] = r
	}
	r.count++
	r.lastFailAt = time.Now()
}

// RecordSuccess clears the failure record for videoID.
func (t *VideoFailureTracker) RecordSuccess(videoID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, videoID)
}

// IsBlocked returns true if videoID has reached maxFailuresPerVideo failures and the
// cooldown window has not yet elapsed. If the cooldown has passed the stale record is
// deleted and the function returns false (allowing a fresh attempt).
func (t *VideoFailureTracker) IsBlocked(videoID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.records[videoID]
	if !ok {
		return false
	}
	if r.count < maxFailuresPerVideo {
		return false
	}
	if time.Since(r.lastFailAt) >= failureCooldown {
		delete(t.records, videoID)
		return false
	}
	return true
}

// inflightEntry holds a job's ID and a channel that is closed when the job finishes.
type inflightEntry struct {
	jobID string
	done  chan struct{}
}

// JobRunner orchestrates the full song-generation pipeline with bounded concurrency.
// Each submitted job is queued, then runs asynchronously in a goroutine that must
// first acquire a slot from the semaphore.
type JobRunner struct {
	ytSvc           YouTubeService
	storage         Storage
	processor       ProcessorClient
	shifter         Shifter
	jobStore        *JobStore
	semaphore       chan struct{} // capacity = maxConcurrent
	prewarmInflight sync.Map      // key: videoID → *inflightEntry
	shiftInflight   sync.Map      // key: "videoID|semitones" → *inflightEntry
	failures        *VideoFailureTracker
}

// NewJobRunner creates a JobRunner with bounded concurrency. maxConcurrent is clamped to >= 1.
func NewJobRunner(
	ytSvc YouTubeService,
	storage Storage,
	processor ProcessorClient,
	shifter Shifter,
	jobStore *JobStore,
	maxConcurrent int,
) *JobRunner {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &JobRunner{
		ytSvc: ytSvc, storage: storage, processor: processor, shifter: shifter,
		jobStore:  jobStore,
		semaphore: make(chan struct{}, maxConcurrent),
		failures:  NewVideoFailureTracker(),
	}
}

// SubmitPrewarm registers a prewarm job (stages 1–3: download, separate, melody) and
// runs it asynchronously. Dedup key is videoID. Returns the jobID immediately.
func (r *JobRunner) SubmitPrewarm(videoID string) string {
	if existing, ok := r.prewarmInflight.Load(videoID); ok {
		return existing.(*inflightEntry).jobID
	}

	// Short-circuit blocked videoIDs before doing any work.
	if r.failures.IsBlocked(videoID) {
		zlog.Warn().Str("videoId", videoID).Msg("prewarm blocked: video has exceeded failure cap")
		jobID := newJobID()
		r.jobStore.Create(models.Job{
			ID:        jobID,
			Status:    models.StatusError,
			Message:   "video temporarily unavailable after repeated failures, try again later",
			CreatedAt: time.Now(),
		})
		return jobID
	}

	jobID := newJobID()
	entry := &inflightEntry{jobID: jobID, done: make(chan struct{})}

	actual, loaded := r.prewarmInflight.LoadOrStore(videoID, entry)
	if loaded {
		return actual.(*inflightEntry).jobID
	}

	r.jobStore.Create(models.Job{
		ID:        jobID,
		Status:    models.StatusQueued,
		CreatedAt: time.Now(),
	})

	go func() {
		defer func() {
			close(entry.done)
			r.prewarmInflight.Delete(videoID)
		}()

		r.semaphore <- struct{}{}
		defer func() { <-r.semaphore }()
		r.runPrewarm(context.Background(), jobID, videoID)
	}()

	return jobID
}

// Submit registers a shift job (stage 4: rubberband shift) and runs it asynchronously.
// Dedup key is (videoID, semitones). Returns the jobID immediately.
//
// If a prewarm job for the same videoID is in flight, the shift goroutine awaits its
// completion before running stage 4. If no prewarm has run, runPrewarm is called inline
// (cache short-circuits any already-completed stages).
func (r *JobRunner) Submit(videoID string, semitones int) string {
	key := videoID + "|" + strconv.Itoa(semitones)

	if existing, ok := r.shiftInflight.Load(key); ok {
		return existing.(*inflightEntry).jobID
	}

	// Short-circuit blocked videoIDs before doing any work.
	if r.failures.IsBlocked(videoID) {
		zlog.Warn().Str("videoId", videoID).Msg("generate blocked: video has exceeded failure cap")
		jobID := newJobID()
		r.jobStore.Create(models.Job{
			ID:        jobID,
			Status:    models.StatusError,
			Message:   "video temporarily unavailable after repeated failures, try again later",
			CreatedAt: time.Now(),
		})
		return jobID
	}

	jobID := newJobID()
	entry := &inflightEntry{jobID: jobID, done: make(chan struct{})}

	actual, loaded := r.shiftInflight.LoadOrStore(key, entry)
	if loaded {
		return actual.(*inflightEntry).jobID
	}

	r.jobStore.Create(models.Job{
		ID:        jobID,
		Status:    models.StatusQueued,
		CreatedAt: time.Now(),
	})

	go func() {
		defer func() {
			close(entry.done)
			r.shiftInflight.Delete(key)
		}()

		r.semaphore <- struct{}{}
		defer func() { <-r.semaphore }()

		ctx := context.Background()

		// If a prewarm is in flight for this videoID, wait for it to finish before
		// running stage 4. The prewarm's done channel is closed when it exits.
		if pw, ok := r.prewarmInflight.Load(videoID); ok {
			<-pw.(*inflightEntry).done
		} else {
			// No in-flight prewarm; run stages 1–3 ourselves (cache skips done stages).
			r.runPrewarm(ctx, jobID, videoID)
			// Check if prewarm already set the job to error.
			if j, ok := r.jobStore.Get(jobID); ok && j.Status == models.StatusError {
				return
			}
		}

		r.runShift(ctx, jobID, videoID, semitones)
	}()

	return jobID
}

// runPrewarm executes stages 1–3 synchronously. Exported via Run for tests.
func (r *JobRunner) runPrewarm(ctx context.Context, jobID, videoID string) {
	failAndRecord := func(msg string) {
		r.failures.RecordFailure(videoID)
		r.fail(jobID, msg)
	}

	// Stage 1: Download full audio.
	r.update(jobID, models.StatusDownloading, "downloading full song")
	has, err := r.storage.Has(ctx, r.storage.Key(videoID, "original"+AudioExt))
	if err != nil {
		r.fail(jobID, "storage check failed: "+err.Error())
		return
	}
	if !has {
		if err := Retry(ctx, PipelineRetryAttempts, PipelineRetryBaseDelay, func() error {
			return r.ytSvc.DownloadFull(ctx, videoID)
		}); err != nil {
			failAndRecord("download failed: " + err.Error())
			return
		}
	}

	// Stage 2: Separate vocals from instrumental.
	r.update(jobID, models.StatusSeparating, "separating vocals from instrumental")
	vocalsKey := r.storage.Key(videoID, "vocals"+AudioExt)
	noVocalsKey := r.storage.Key(videoID, "no_vocals"+AudioExt)
	vocalsHas, _ := r.storage.Has(ctx, vocalsKey)
	noVocalsHas, _ := r.storage.Has(ctx, noVocalsKey)
	if !vocalsHas || !noVocalsHas {
		inURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "original"+AudioExt))
		if err != nil {
			r.fail(jobID, "sign get failed: "+err.Error())
			return
		}
		vocalsPutURL, err := r.storage.SignPut(ctx, vocalsKey)
		if err != nil {
			r.fail(jobID, "sign put failed: "+err.Error())
			return
		}
		noVocalsPutURL, err := r.storage.SignPut(ctx, noVocalsKey)
		if err != nil {
			r.fail(jobID, "sign put failed: "+err.Error())
			return
		}
		if err := Retry(ctx, PipelineRetryAttempts, PipelineRetryBaseDelay, func() error {
			return r.processor.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL)
		}); err != nil {
			failAndRecord("separate failed: " + err.Error())
			return
		}
		if err := r.storage.Verify(ctx, vocalsKey); err != nil {
			failAndRecord("vocals stem not materialized: " + err.Error())
			return
		}
		if err := r.storage.Verify(ctx, noVocalsKey); err != nil {
			failAndRecord("no_vocals stem not materialized: " + err.Error())
			return
		}
	}

	// Stage 3: Extract melody from vocals stem.
	r.update(jobID, models.StatusMelody, "extracting melody")
	melodyKey := r.storage.Key(videoID, "melody.json")
	melodyHas, _ := r.storage.Has(ctx, melodyKey)
	if !melodyHas {
		vocalsURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "vocals"+AudioExt))
		if err != nil {
			r.fail(jobID, "sign get failed: "+err.Error())
			return
		}
		outURL, err := r.storage.SignPut(ctx, melodyKey)
		if err != nil {
			r.fail(jobID, "sign put failed: "+err.Error())
			return
		}
		if err := Retry(ctx, PipelineRetryAttempts, PipelineRetryBaseDelay, func() error {
			return r.processor.Melody(ctx, vocalsURL, outURL)
		}); err != nil {
			failAndRecord("melody failed: " + err.Error())
			return
		}
		if err := r.storage.Verify(ctx, melodyKey); err != nil {
			failAndRecord("melody not materialized: " + err.Error())
			return
		}
	}

	r.failures.RecordSuccess(videoID)
	r.update(jobID, models.StatusDone, "stems ready")
}

// runShift executes stage 4 synchronously.
func (r *JobRunner) runShift(ctx context.Context, jobID, videoID string, semitones int) {
	// Hard ceiling so a hung rubberband/ffmpeg never wedges the inflight slot.
	ctx, cancel := context.WithTimeout(ctx, ShiftStageTimeout)
	defer cancel()

	failAndRecord := func(msg string) {
		r.failures.RecordFailure(videoID)
		r.fail(jobID, msg)
	}

	r.update(jobID, models.StatusShifting, "shifting instrumental to your key")
	shiftedName := "shifted/" + strconv.Itoa(semitones) + "/audio" + AudioExt
	shiftedKey := r.storage.Key(videoID, shiftedName)
	shiftedHas, _ := r.storage.Has(ctx, shiftedKey)
	if !shiftedHas {
		noVocalsKey := r.storage.Key(videoID, "no_vocals"+AudioExt)
		rc, err := r.storage.Open(ctx, noVocalsKey)
		if err != nil {
			failAndRecord("open no_vocals: " + err.Error())
			return
		}
		scratchIn, err := os.CreateTemp("", "cantus-shift-in-*"+AudioExt)
		if err != nil {
			_ = rc.Close()
			failAndRecord("tempfile in: " + err.Error())
			return
		}
		if _, err := io.Copy(scratchIn, rc); err != nil {
			_ = rc.Close()
			_ = scratchIn.Close()
			_ = os.Remove(scratchIn.Name())
			failAndRecord("copy no_vocals to scratch: " + err.Error())
			return
		}
		_ = rc.Close()
		_ = scratchIn.Close()
		defer func() { _ = os.Remove(scratchIn.Name()) }()

		scratchOut, err := os.CreateTemp("", "cantus-shift-out-*"+AudioExt)
		if err != nil {
			failAndRecord("tempfile out: " + err.Error())
			return
		}
		_ = scratchOut.Close()
		defer func() { _ = os.Remove(scratchOut.Name()) }()

		if err := Retry(ctx, PipelineRetryAttempts, PipelineRetryBaseDelay, func() error {
			return r.shifter.Shift(ctx, scratchIn.Name(), scratchOut.Name(), float64(semitones))
		}); err != nil {
			failAndRecord("shift failed: " + err.Error())
			return
		}
		if err := r.storage.Commit(ctx, shiftedKey, scratchOut.Name()); err != nil {
			failAndRecord("commit shifted: " + err.Error())
			return
		}
		if err := r.storage.Verify(ctx, shiftedKey); err != nil {
			failAndRecord("shift output not materialized: " + err.Error())
			return
		}
	}

	r.failures.RecordSuccess(videoID)
	r.update(jobID, models.StatusDone, "ready to sing")
}

// Run executes the full pipeline synchronously. Exported so tests can drive it directly.
func (r *JobRunner) Run(ctx context.Context, jobID, videoID string, semitones int) {
	r.runPrewarm(ctx, jobID, videoID)
	if j, ok := r.jobStore.Get(jobID); ok && j.Status == models.StatusError {
		return
	}
	r.runShift(ctx, jobID, videoID, semitones)
}

// update sets the job's Status and Message fields atomically.
func (r *JobRunner) update(jobID string, status models.JobStatus, message string) {
	r.jobStore.Update(jobID, func(j *models.Job) {
		j.Status = status
		j.Message = message
	})
}

// fail sets the job's status to StatusError with the given message.
func (r *JobRunner) fail(jobID, message string) {
	r.jobStore.Update(jobID, func(j *models.Job) {
		j.Status = models.StatusError
		j.Message = message
	})
}

// Retry attempts op up to `attempts` times with exponential backoff starting at baseDelay.
// It returns immediately if ctx is canceled. On each failure it logs the attempt number
// and error. Returns nil on first success, or the last error if all attempts fail.
func Retry(ctx context.Context, attempts int, baseDelay time.Duration, op func() error) error {
	var err error
	delay := baseDelay
	for i := 1; i <= attempts; i++ {
		err = op()
		if err == nil {
			return nil
		}
		zlog.Warn().Err(err).Int("attempt", i).Int("max_attempts", attempts).Msg("pipeline stage failed, will retry")
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return err
}

// newJobID generates a random 32-character hex string using crypto/rand.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
