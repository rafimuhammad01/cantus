package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cantus/backend/api"
)

func TestRouter_Health(t *testing.T) {
	router := api.NewRouter([]string{"http://localhost:5173"})

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
	router := api.NewRouter([]string{"http://localhost:5173"})

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
	router := api.NewRouter([]string{"http://localhost:5173"})

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
	router := api.NewRouter([]string{"http://localhost:5173"})

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
