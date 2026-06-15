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
)

// JobRunner orchestrates the full song-generation pipeline with bounded concurrency.
// Each submitted job is queued, then runs asynchronously in a goroutine that must
// first acquire a slot from the semaphore.
type JobRunner struct {
	ytSvc     YouTubeService
	storage   Storage
	processor ProcessorClient
	shifter   Shifter
	jobStore  *JobStore
	semaphore chan struct{} // capacity = maxConcurrent
	inflight  sync.Map      // key: "videoID:semitones" (string) → value: jobID (string)
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
	}
}

// Submit registers a new job in the JobStore and runs the pipeline asynchronously.
// It returns the job ID immediately; the goroutine blocks on the semaphore until a
// slot is available before starting.
//
// Dedup key is (videoID, semitones). A repeat submit for the same pair returns the
// in-flight jobID without spawning a second goroutine. Different semitones for the
// same videoID start independent jobs — Separate output is cached, so the second
// job typically only re-runs the cheap Shift stage. With MAX_CONCURRENT_JOBS=1
// (default) jobs serialize on the semaphore, so the second job naturally sees the
// first's stems already committed.
func (r *JobRunner) Submit(videoID string, semitones int) string {
	key := videoID + ":" + strconv.Itoa(semitones)

	// Fast path: avoid crypto/rand + hex encode when this (videoID, semitones) is
	// already in-flight.
	if existing, ok := r.inflight.Load(key); ok {
		return existing.(string)
	}

	jobID := newJobID()

	actual, loaded := r.inflight.LoadOrStore(key, jobID)
	if loaded {
		// Another goroutine won the race between Load and LoadOrStore; return its jobID.
		return actual.(string)
	}

	// We own the inflight slot. Register the job and start the goroutine.
	r.jobStore.Create(models.Job{
		ID:        jobID,
		Status:    models.StatusQueued,
		CreatedAt: time.Now(),
	})

	go func() {
		// Delete the inflight entry when the goroutine exits — covers both normal
		// completion and panics — so future submits for this key start fresh.
		defer r.inflight.Delete(key)

		r.semaphore <- struct{}{}
		defer func() { <-r.semaphore }()
		r.Run(context.Background(), jobID, videoID, semitones)
	}()

	return jobID
}

// Run executes the four-stage pipeline synchronously. It is exported so tests can
// drive it directly without goroutines.
func (r *JobRunner) Run(ctx context.Context, jobID, videoID string, semitones int) {
	// Stage 1: Download full audio.
	r.update(jobID, models.StatusDownloading, "downloading full song")
	has, err := r.storage.Has(ctx, r.storage.Key(videoID, "original.wav"))
	if err != nil {
		r.fail(jobID, "storage check failed: "+err.Error())
		return
	}
	if !has {
		if err := r.ytSvc.DownloadFull(ctx, videoID); err != nil {
			r.fail(jobID, "download failed: "+err.Error())
			return
		}
	}

	// Stage 2: Separate vocals from instrumental.
	r.update(jobID, models.StatusSeparating, "separating vocals from instrumental")
	vocalsKey := r.storage.Key(videoID, "vocals.wav")
	noVocalsKey := r.storage.Key(videoID, "no_vocals.wav")
	vocalsHas, _ := r.storage.Has(ctx, vocalsKey)
	noVocalsHas, _ := r.storage.Has(ctx, noVocalsKey)
	if !vocalsHas || !noVocalsHas {
		inURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "original.wav"))
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
		if err := r.processor.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL); err != nil {
			r.fail(jobID, "separate failed: "+err.Error())
			return
		}
		if err := r.storage.Verify(ctx, vocalsKey); err != nil {
			r.fail(jobID, "vocals stem not materialized: "+err.Error())
			return
		}
		if err := r.storage.Verify(ctx, noVocalsKey); err != nil {
			r.fail(jobID, "no_vocals stem not materialized: "+err.Error())
			return
		}
	}

	// Stage 3: Extract melody from vocals stem.
	r.update(jobID, models.StatusMelody, "extracting melody")
	melodyKey := r.storage.Key(videoID, "melody.json")
	melodyHas, _ := r.storage.Has(ctx, melodyKey)
	if !melodyHas {
		vocalsURL, err := r.storage.SignGet(ctx, r.storage.Key(videoID, "vocals.wav"))
		if err != nil {
			r.fail(jobID, "sign get failed: "+err.Error())
			return
		}
		outURL, err := r.storage.SignPut(ctx, melodyKey)
		if err != nil {
			r.fail(jobID, "sign put failed: "+err.Error())
			return
		}
		if err := r.processor.Melody(ctx, vocalsURL, outURL); err != nil {
			r.fail(jobID, "melody failed: "+err.Error())
			return
		}
		if err := r.storage.Verify(ctx, melodyKey); err != nil {
			r.fail(jobID, "melody not materialized: "+err.Error())
			return
		}
	}

	// Stage 4: Pitch-shift the instrumental stem to the requested key.
	r.update(jobID, models.StatusShifting, "shifting instrumental to your key")
	shiftedName := "shifted/" + strconv.Itoa(semitones) + "/audio.mp3"
	shiftedKey := r.storage.Key(videoID, shiftedName)
	shiftedHas, _ := r.storage.Has(ctx, shiftedKey)
	if !shiftedHas {
		noVocalsKey := r.storage.Key(videoID, "no_vocals.wav")
		rc, err := r.storage.Open(ctx, noVocalsKey)
		if err != nil {
			r.fail(jobID, "open no_vocals: "+err.Error())
			return
		}
		scratchIn, err := os.CreateTemp("", "cantus-shift-in-*.wav")
		if err != nil {
			_ = rc.Close()
			r.fail(jobID, "tempfile in: "+err.Error())
			return
		}
		if _, err := io.Copy(scratchIn, rc); err != nil {
			_ = rc.Close()
			_ = scratchIn.Close()
			_ = os.Remove(scratchIn.Name())
			r.fail(jobID, "copy no_vocals to scratch: "+err.Error())
			return
		}
		_ = rc.Close()
		_ = scratchIn.Close()
		defer func() { _ = os.Remove(scratchIn.Name()) }()

		scratchOut, err := os.CreateTemp("", "cantus-shift-out-*.mp3")
		if err != nil {
			r.fail(jobID, "tempfile out: "+err.Error())
			return
		}
		_ = scratchOut.Close()
		defer func() { _ = os.Remove(scratchOut.Name()) }()

		if err := r.shifter.Shift(ctx, scratchIn.Name(), scratchOut.Name(), float64(semitones)); err != nil {
			r.fail(jobID, "shift failed: "+err.Error())
			return
		}
		if err := r.storage.Commit(ctx, shiftedKey, scratchOut.Name()); err != nil {
			r.fail(jobID, "commit shifted: "+err.Error())
			return
		}
		if err := r.storage.Verify(ctx, shiftedKey); err != nil {
			r.fail(jobID, "shift output not materialized: "+err.Error())
			return
		}
	}

	r.update(jobID, models.StatusDone, "ready to sing")
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

// newJobID generates a random 32-character hex string using crypto/rand.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
