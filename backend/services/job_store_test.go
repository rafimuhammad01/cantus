package services_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"cantus/backend/models"
	"cantus/backend/services"
)

// newTestJob is a helper that builds a minimal Job with the given id and CreatedAt.
func newTestJob(id string, createdAt time.Time) models.Job {
	return models.Job{
		ID:        id,
		Status:    models.StatusQueued,
		CreatedAt: createdAt,
	}
}

// TestJobStore_CreateAndGet verifies that Create stores a job and Get retrieves it
// correctly, and that Get returns ok=false for an unknown id.
func TestJobStore_CreateAndGet(t *testing.T) {
	store := services.NewJobStore(24 * time.Hour)

	now := time.Now()
	jobA := newTestJob("job-a", now)
	jobB := newTestJob("job-b", now)
	store.Create(jobA)
	store.Create(jobB)

	tests := []struct {
		name       string
		id         string
		wantID     string
		wantOK     bool
		wantStatus models.JobStatus
	}{
		{
			name:       "existing job A",
			id:         "job-a",
			wantID:     "job-a",
			wantOK:     true,
			wantStatus: models.StatusQueued,
		},
		{
			name:       "existing job B",
			id:         "job-b",
			wantID:     "job-b",
			wantOK:     true,
			wantStatus: models.StatusQueued,
		},
		{
			name:       "non-existent id returns zero value and false",
			id:         "does-not-exist",
			wantID:     "",
			wantOK:     false,
			wantStatus: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := store.Get(tt.id)

			if ok != tt.wantOK {
				t.Errorf("Get(%q): ok = %v, want %v", tt.id, ok, tt.wantOK)
			}
			if got.ID != tt.wantID {
				t.Errorf("Get(%q): ID = %q, want %q", tt.id, got.ID, tt.wantID)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Get(%q): Status = %q, want %q", tt.id, got.Status, tt.wantStatus)
			}
		})
	}
}

// TestJobStore_Update verifies that Update applies changes to an existing job and
// reports false (without running the closure) for a missing id.
func TestJobStore_Update(t *testing.T) {
	tests := []struct {
		name       string
		targetID   string
		wantReturn bool
	}{
		{
			name:       "existing job — closure runs and changes are persisted",
			targetID:   "job-update-existing",
			wantReturn: true,
		},
		{
			name:       "missing job — closure is not invoked",
			targetID:   "job-does-not-exist",
			wantReturn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := services.NewJobStore(24 * time.Hour)
			store.Create(newTestJob("job-update-existing", time.Now()))

			closureCalls := 0

			got := store.Update(tt.targetID, func(j *models.Job) {
				closureCalls++
				j.Status = models.StatusSeparating
				j.Message = "separating"
			})

			if got != tt.wantReturn {
				t.Errorf("Update(%q): returned %v, want %v", tt.targetID, got, tt.wantReturn)
			}

			if tt.wantReturn {
				// Closure must have been called exactly once.
				if closureCalls != 1 {
					t.Errorf("Update(%q): closure called %d times, want 1", tt.targetID, closureCalls)
				}
				// Changes must be visible via Get.
				j, ok := store.Get(tt.targetID)
				if !ok {
					t.Fatalf("Get(%q) after Update: ok = false, want true", tt.targetID)
				}
				if j.Status != models.StatusSeparating {
					t.Errorf("Get(%q).Status = %q, want %q", tt.targetID, j.Status, models.StatusSeparating)
				}
				if j.Message != "separating" {
					t.Errorf("Get(%q).Message = %q, want %q", tt.targetID, j.Message, "separating")
				}
			} else {
				// Closure must never have been called.
				if closureCalls != 0 {
					t.Errorf("Update(%q) on missing id: closure called %d times, want 0", tt.targetID, closureCalls)
				}
			}
		})
	}
}

// TestJobStore_Delete verifies that Delete removes an existing job and is a safe
// no-op for an id that was never created.
func TestJobStore_Delete(t *testing.T) {
	tests := []struct {
		name        string
		createFirst bool
		id          string
		wantOKAfter bool
	}{
		{
			name:        "delete existing",
			createFirst: true,
			id:          "job-to-delete",
			wantOKAfter: false,
		},
		{
			name:        "delete non-existing does not panic",
			createFirst: false,
			id:          "never-created",
			wantOKAfter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := services.NewJobStore(24 * time.Hour)

			if tt.createFirst {
				store.Create(newTestJob(tt.id, time.Now()))
			}

			// Must not panic regardless of whether the job existed.
			store.Delete(tt.id)

			_, ok := store.Get(tt.id)
			if ok != tt.wantOKAfter {
				t.Errorf("Get(%q) after Delete: ok = %v, want %v", tt.id, ok, tt.wantOKAfter)
			}
		})
	}
}

// TestJobStore_Cleanup_EvictsStaleAndKeepsFresh verifies that Cleanup removes only
// jobs whose CreatedAt is older than ttl and returns an accurate eviction count.
func TestJobStore_Cleanup_EvictsStaleAndKeepsFresh(t *testing.T) {
	const ttl = time.Hour

	tests := []struct {
		name                    string
		age                     time.Duration // how long ago was the job created
		wantPresentAfterCleanup bool
	}{
		{
			name:                    "fresh job — created now",
			age:                     0,
			wantPresentAfterCleanup: true,
		},
		{
			name:                    "stale job — created 2 hours ago",
			age:                     2 * time.Hour,
			wantPresentAfterCleanup: false,
		},
	}

	store := services.NewJobStore(ttl)
	ids := make([]string, len(tests))

	for i, tt := range tests {
		id := "job-cleanup-" + tt.name
		ids[i] = id
		store.Create(newTestJob(id, time.Now().Add(-tt.age)))
	}

	wantEvicted := 0
	for _, tt := range tests {
		if !tt.wantPresentAfterCleanup {
			wantEvicted++
		}
	}

	evicted := store.Cleanup()
	if evicted != wantEvicted {
		t.Errorf("Cleanup(): evicted %d, want %d", evicted, wantEvicted)
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := store.Get(ids[i])
			if ok != tt.wantPresentAfterCleanup {
				t.Errorf("Get(%q) after Cleanup: ok = %v, want %v", ids[i], ok, tt.wantPresentAfterCleanup)
			}
		})
	}
}

// TestJobStore_StartCleanup_RunsPeriodicallyAndStopsOnContextCancel verifies that
// StartCleanup evicts stale records on a periodic interval and halts when the
// context is cancelled.
func TestJobStore_StartCleanup_RunsPeriodicallyAndStopsOnContextCancel(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "evicts then stops on cancel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const ttl = time.Millisecond

			store := services.NewJobStore(ttl)

			// Insert a stale job (created 1 second in the past — well past 1 ms TTL).
			staleID := "job-start-cleanup-stale"
			store.Create(newTestJob(staleID, time.Now().Add(-time.Second)))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			store.StartCleanup(ctx, 5*time.Millisecond)

			// Poll up to ~500 ms for the stale job to be evicted.
			evicted := false
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				if _, ok := store.Get(staleID); !ok {
					evicted = true
					break
				}
				time.Sleep(20 * time.Millisecond)
			}

			if !evicted {
				t.Fatalf("StartCleanup: stale job %q was not evicted within 500 ms", staleID)
			}

			// Cancel the context to stop the cleanup goroutine.
			cancel()

			// Give the goroutine a moment to observe the cancellation.
			time.Sleep(50 * time.Millisecond)

			// Insert another stale job; it should NOT be cleaned up after the goroutine stops.
			afterCancelID := "job-start-cleanup-after-cancel"
			store.Create(newTestJob(afterCancelID, time.Now().Add(-time.Second)))

			time.Sleep(50 * time.Millisecond)

			_, stillPresent := store.Get(afterCancelID)
			if !stillPresent {
				t.Errorf("StartCleanup: job %q was cleaned up after context cancel, want goroutine stopped", afterCancelID)
			}
		})
	}
}

// TestJobStore_ConcurrentAccess is a race-detector smoke test. Multiple goroutines
// each create, update, get, and delete their own job. After all goroutines complete
// none of their jobs should remain in the store.
func TestJobStore_ConcurrentAccess(t *testing.T) {
	tests := []struct {
		name       string
		goroutines int
	}{
		{
			name:       "20 concurrent goroutines with no data races",
			goroutines: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := services.NewJobStore(24 * time.Hour)

			var wg sync.WaitGroup
			ids := make([]string, tt.goroutines)

			for i := 0; i < tt.goroutines; i++ {
				id := "concurrent-job-" + string(rune('A'+i))
				ids[i] = id

				wg.Add(1)
				go func(jobID string) {
					defer wg.Done()

					store.Create(newTestJob(jobID, time.Now()))

					store.Update(jobID, func(j *models.Job) {
						j.Status = models.StatusSeparating
						j.Message = "separating"
					})

					store.Get(jobID)

					store.Delete(jobID)
				}(id)
			}

			wg.Wait()

			// All jobs should have been deleted — count any survivors.
			survivors := 0
			for _, id := range ids {
				if _, ok := store.Get(id); ok {
					survivors++
				}
			}

			if survivors != 0 {
				t.Errorf("after all goroutines finished: %d job(s) remain in store, want 0", survivors)
			}
		})
	}
}
