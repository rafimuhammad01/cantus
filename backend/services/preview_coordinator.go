package services

import (
	"context"
	"strconv"
	"sync"
)

// previewInflightEntry holds a done channel that is closed when the in-flight
// preview operation completes.
type previewInflightEntry struct {
	done chan struct{}
	err  error
}

// PreviewCoordinator deduplicates concurrent preview-stems and preview-shift
// requests. A second caller for the same key waits on the first caller's done
// channel and then re-checks the cache, matching the pattern used by JobRunner
// for the full-song pipeline (prewarmInflight / shiftInflight).
type PreviewCoordinator struct {
	stemsInflight sync.Map // key: videoID → *previewInflightEntry
	shiftInflight sync.Map // key: "videoID|semitones" → *previewInflightEntry
}

// NewPreviewCoordinator returns an initialised PreviewCoordinator.
func NewPreviewCoordinator() *PreviewCoordinator {
	return &PreviewCoordinator{}
}

// RunPreviewStems calls fn for the first caller with videoID. Concurrent
// callers with the same videoID wait for the first call to complete, then
// return its result. This prevents duplicate BS-Roformer/CREPE runs.
func (c *PreviewCoordinator) RunPreviewStems(ctx context.Context, videoID string, fn func(ctx context.Context) error) error {
	entry := &previewInflightEntry{done: make(chan struct{})}

	actual, loaded := c.stemsInflight.LoadOrStore(videoID, entry)
	if loaded {
		// Another goroutine is already running. Wait for it, honouring ctx.
		existing := actual.(*previewInflightEntry)
		select {
		case <-existing.done:
			return existing.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// We own the in-flight entry. Run fn and close done when finished.
	defer func() {
		c.stemsInflight.Delete(videoID)
		close(entry.done)
	}()

	entry.err = fn(ctx)
	return entry.err
}

// RunPreviewShift calls fn for the first caller with (videoID, semitones).
// Concurrent callers with the same key wait for the first call to complete,
// then return its result.
func (c *PreviewCoordinator) RunPreviewShift(ctx context.Context, videoID string, semitones int, fn func(ctx context.Context) error) error {
	key := videoID + "|" + strconv.Itoa(semitones)

	entry := &previewInflightEntry{done: make(chan struct{})}

	actual, loaded := c.shiftInflight.LoadOrStore(key, entry)
	if loaded {
		existing := actual.(*previewInflightEntry)
		select {
		case <-existing.done:
			return existing.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	defer func() {
		c.shiftInflight.Delete(key)
		close(entry.done)
	}()

	entry.err = fn(ctx)
	return entry.err
}
