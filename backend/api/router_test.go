package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cantus/backend/api"
	"cantus/backend/logger"
	"cantus/backend/services"
)

// fakeYouTubeService is a no-op stub that satisfies services.YouTubeService for router tests.
type fakeYouTubeService struct{}

func (f *fakeYouTubeService) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return services.SearchPage{}, nil
}

func (f *fakeYouTubeService) DownloadPreview(_ context.Context, _ string) error { return nil }
func (f *fakeYouTubeService) DownloadFull(_ context.Context, _ string) error    { return nil }

func TestRouter_Health(t *testing.T) {
	log, err := logger.New(io.Discard, "info")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type: got %q, want to contain application/json", ct)
	}

	var resp struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status field: got %q, want %q", resp.Status, "ok")
	}
}

func TestRouter_CORS_AllowedOrigin(t *testing.T) {
	log, err := logger.New(io.Discard, "info")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}

	if h := rec.Header().Get("Access-Control-Allow-Origin"); h != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want %q", h, "http://localhost:5173")
	}
}

func TestRouter_CORS_DisallowedOrigin(t *testing.T) {
	log, err := logger.New(io.Discard, "info")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://evil.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}

	if h := rec.Header().Get("Access-Control-Allow-Origin"); h != "" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want empty string for disallowed origin", h)
	}
}

func TestRouter_CORS_Preflight(t *testing.T) {
	log, err := logger.New(io.Discard, "info")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK && got != http.StatusNoContent {
		t.Errorf("status: got %d, want 200 or 204", got)
	}

	if h := rec.Header().Get("Access-Control-Allow-Origin"); h != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want %q", h, "http://localhost:5173")
	}

	if h := rec.Header().Get("Access-Control-Allow-Methods"); h == "" {
		t.Errorf("Access-Control-Allow-Methods: got empty string, want non-empty")
	} else if !strings.Contains(h, "GET") {
		t.Errorf("Access-Control-Allow-Methods: got %q, want it to contain GET", h)
	}
}

// TestRouter_SetsRequestIDHeader verifies that the logger middleware sets the
// X-Request-ID response header on every request.
func TestRouter_SetsRequestIDHeader(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "X-Request-ID header set on response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, err := logger.New(io.Discard, "info")
			if err != nil {
				t.Fatalf("logger.New: %v", err)
			}
			router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if id := rec.Header().Get("X-Request-ID"); id == "" {
				t.Errorf("X-Request-ID header: got empty string, want non-empty request ID")
			}
		})
	}
}

// TestRouter_LogsRequest verifies that the logger middleware emits a JSON log
// line per request containing method, path, status, duration_ms, and
// request_id fields.
func TestRouter_LogsRequest(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "request line logged with method path status request_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log, err := logger.New(&buf, "info")
			if err != nil {
				t.Fatalf("logger.New: %v", err)
			}
			router := api.NewRouter([]string{"http://localhost:5173"}, log, &fakeYouTubeService{}, nil, nil, nil, nil, nil)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// Scan log lines for the request entry.
			output := buf.String()
			lines := strings.Split(strings.TrimSpace(output), "\n")

			found := false
			for _, line := range lines {
				if line == "" {
					continue
				}
				var entry map[string]interface{}
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					continue
				}

				method, hasMethod := entry["method"]
				path, hasPath := entry["path"]
				_, hasStatus := entry["status"]
				_, hasDuration := entry["duration_ms"]
				requestID, hasRequestID := entry["request_id"]

				if !hasMethod || !hasPath || !hasStatus || !hasDuration || !hasRequestID {
					continue
				}
				if method != "GET" || path != "/health" {
					continue
				}
				statusVal, ok := entry["status"].(float64)
				if !ok || int(statusVal) != http.StatusOK {
					continue
				}
				if rid, ok := requestID.(string); !ok || rid == "" {
					continue
				}

				found = true
				break
			}

			if !found {
				t.Fatalf("no log entry with method=GET path=/health status=200 duration_ms request_id found.\nBuffer contents:\n%s", output)
			}
		})
	}
}
