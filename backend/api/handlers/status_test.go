package handlers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"cantus/backend/api/handlers"
	"cantus/backend/models"
	"cantus/backend/services"
)

// statusRouter wires a chi router with the Status handler at GET /api/status/{jobId}.
func statusRouter(jobStore *services.JobStore) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/status/{jobId}", handlers.Status(jobStore))
	return r
}

func TestStatusHandler_StreamsTransitions(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "queued → downloading → done"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobStore := services.NewJobStore(time.Hour)
			jobID := "test-job-001"
			jobStore.Create(models.Job{ID: jobID, Status: models.StatusQueued, CreatedAt: time.Now()})

			r := chi.NewRouter()
			r.Get("/api/status/{jobId}", handlers.Status(jobStore))
			srv := httptest.NewServer(r)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/status/" + jobID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
				t.Errorf("Content-Type: got %q, want text/event-stream", got)
			}

			go func() {
				time.Sleep(500 * time.Millisecond)
				jobStore.Update(jobID, func(j *models.Job) {
					j.Status = models.StatusDownloading
					j.Message = "downloading"
				})
				time.Sleep(500 * time.Millisecond)
				jobStore.Update(jobID, func(j *models.Job) {
					j.Status = models.StatusDone
					j.Message = "ready"
				})
			}()

			deadline := time.Now().Add(8 * time.Second)
			scanner := bufio.NewScanner(resp.Body)
			var events []map[string]any
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := strings.TrimPrefix(line, "data: ")
				var ev map[string]any
				if err := json.Unmarshal([]byte(payload), &ev); err != nil {
					continue
				}
				events = append(events, ev)
				if ev["status"] == "done" || ev["status"] == "error" {
					break
				}
				if time.Now().After(deadline) {
					break
				}
			}

			if len(events) < 2 {
				t.Fatalf("expected ≥2 events, got %d: %v", len(events), events)
			}
			if events[0]["status"] != "queued" {
				t.Errorf("first event status: got %v, want queued", events[0]["status"])
			}
			if events[len(events)-1]["status"] != "done" {
				t.Errorf("last event status: got %v, want done", events[len(events)-1]["status"])
			}
		})
	}
}

func TestStatusHandler_404OnUnknownJob(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "unknown job returns 404"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobStore := services.NewJobStore(time.Hour)
			router := statusRouter(jobStore)

			req := httptest.NewRequest(http.MethodGet, "/api/status/nonexistent", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if got, want := rec.Code, http.StatusNotFound; got != want {
				t.Errorf("status: got %d, want %d (body: %s)", got, want, rec.Body.String())
			}

			if !strings.Contains(rec.Body.String(), "job not found") {
				t.Errorf("body: got %q, want it to contain 'job not found'", rec.Body.String())
			}
		})
	}
}

func TestStatusHandler_TerminatesOnError(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "error job sends error event then closes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobStore := services.NewJobStore(time.Hour)
			jobID := "error-job-001"
			jobStore.Create(models.Job{
				ID:        jobID,
				Status:    models.StatusError,
				Message:   "pipeline failed",
				CreatedAt: time.Now(),
			})

			r := chi.NewRouter()
			r.Get("/api/status/{jobId}", handlers.Status(jobStore))
			srv := httptest.NewServer(r)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/status/" + jobID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			deadline := time.Now().Add(5 * time.Second)
			scanner := bufio.NewScanner(resp.Body)
			var events []map[string]any
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := strings.TrimPrefix(line, "data: ")
				var ev map[string]any
				if err := json.Unmarshal([]byte(payload), &ev); err != nil {
					continue
				}
				events = append(events, ev)
				if ev["status"] == "done" || ev["status"] == "error" {
					break
				}
				if time.Now().After(deadline) {
					break
				}
			}

			if len(events) < 1 {
				t.Fatalf("expected ≥1 event, got 0")
			}
			found := false
			for _, ev := range events {
				if ev["status"] == "error" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("events: no error status found in %v", events)
			}
		})
	}
}

func TestStatusHandler_ClientDisconnect(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "client disconnect causes server to stop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobStore := services.NewJobStore(time.Hour)
			jobID := "disconnect-job-001"
			jobStore.Create(models.Job{
				ID:        jobID,
				Status:    models.StatusQueued,
				CreatedAt: time.Now(),
			})

			r := chi.NewRouter()
			r.Get("/api/status/{jobId}", handlers.Status(jobStore))
			srv := httptest.NewServer(r)
			defer srv.Close()

			ctx, cancel := context.WithCancel(context.Background())

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/status/"+jobID, nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext: %v", err)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			// Read the initial event.
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					break
				}
			}

			// Cancel the context (simulate client disconnect).
			cancel()

			// The handler should stop within a reasonable timeout.
			done := make(chan struct{})
			go func() {
				defer close(done)
				// Drain any remaining data until the connection closes.
				for scanner.Scan() {
				}
			}()

			select {
			case <-done:
				// server stopped as expected
			case <-time.After(5 * time.Second):
				t.Errorf("handler did not stop after client disconnect within 5s")
			}
		})
	}
}
