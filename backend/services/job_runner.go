package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	cpu       CPUProcessorClient
	gpu       GPUProcessorClient
	processor ProcessorClient
	jobStore  *JobStore
	semaphore chan struct{} // capacity = maxConcurrent
	inflight  sync.Map      // key: videoID (string) → value: jobID (string)
}

// NewJobRunner creates a JobRunner with bounded concurrency. maxConcurrent is clamped to >= 1.
func NewJobRunner(
	ytSvc YouTubeService,
	storage Storage,
	cpu CPUProcessorClient,
	gpu GPUProcessorClient,
	processor ProcessorClient,
	jobStore *JobStore,
	maxConcurrent int,
) *JobRunner {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &JobRunner{
		ytSvc: ytSvc, storage: storage, cpu: cpu, gpu: gpu,
		processor: processor, jobStore: jobStore,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// Submit registers a new job in the JobStore and runs the pipeline asynchronously.
// It returns the job ID immediately; the goroutine blocks on the semaphore until a
// slot is available before starting.
//
// If an identical videoID is already in-flight, Submit returns the existing jobID
// without spawning a second goroutine — deduplication at the videoID level prevents
// parallel Demucs runs racing on the same stem files.
func (r *JobRunner) Submit(videoID string, semitones int) string {
	// Fast path: avoid crypto/rand + hex encode when the videoID is already in-flight.
	if existing, ok := r.inflight.Load(videoID); ok {
		return existing.(string)
	}

	jobID := newJobID()

	actual, loaded := r.inflight.LoadOrStore(videoID, jobID)
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
		// completion and panics — so future submits for this videoID start fresh.
		defer r.inflight.Delete(videoID)

		r.semaphore <- struct{}{}
		defer func() { <-r.semaphore }()
		r.Run(context.Background(), jobID, videoID, semitones)
	}()

	return jobID
}

// Run executes the four-stage pipeline synchronously. It is exported so tests can
// drive it directly without goroutines.
func (r *JobRunner) Run(ctx context.Context, jobID, videoID string, semitones int) {
	// localDisk is used for filesystem path resolution in Stages 3-4 until Task 8
	// migrates Melody to URL handoff.
	localDisk, ok := r.storage.(*LocalDiskStorage)
	if !ok {
		r.fail(jobID, "storage backend does not support local filesystem paths")
		return
	}

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
		if err := r.gpu.Separate(ctx, inURL, vocalsPutURL, noVocalsPutURL); err != nil {
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
	melodyHas, _ := r.storage.Has(ctx, r.storage.Key(videoID, "melody.json"))
	if !melodyHas {
		vocalsPath := localDisk.FilesystemPathForLocalProcessor(r.storage.Key(videoID, "vocals.wav"))
		melodyPath := localDisk.FilesystemPathForLocalProcessor(r.storage.Key(videoID, "melody.json"))
		if err := r.processor.Melody(ctx, vocalsPath, melodyPath); err != nil {
			r.fail(jobID, "melody failed: "+err.Error())
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
		inURL, err := r.storage.SignGet(ctx, noVocalsKey)
		if err != nil {
			r.fail(jobID, "sign get failed: "+err.Error())
			return
		}
		outURL, err := r.storage.SignPut(ctx, shiftedKey)
		if err != nil {
			r.fail(jobID, "sign put failed: "+err.Error())
			return
		}
		if err := r.cpu.Shift(ctx, inURL, outURL, float64(semitones)); err != nil {
			r.fail(jobID, "shift failed: "+err.Error())
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
