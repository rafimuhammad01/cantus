package config_test

import (
	"strings"
	"testing"

	"cantus/backend/config"
)

// TestLoad_HappyPath_WithDefaults verifies that when only the required
// VIDEO_ID_SIGNING_KEY is set, all optional fields assume their documented
// default values.
func TestLoad_HappyPath_WithDefaults(t *testing.T) {
	t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("a", 32))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.PythonProcessorURL != "http://localhost:8090" {
		t.Errorf("PythonProcessorURL: got %q, want %q", cfg.PythonProcessorURL, "http://localhost:8090")
	}
	if cfg.AudioTmpDir != "./tmp" {
		t.Errorf("AudioTmpDir: got %q, want %q", cfg.AudioTmpDir, "./tmp")
	}
	if cfg.CacheDir != "./tmp/cache" {
		t.Errorf("CacheDir: got %q, want %q", cfg.CacheDir, "./tmp/cache")
	}
	if cfg.CacheTTLHours != 24 {
		t.Errorf("CacheTTLHours: got %d, want %d", cfg.CacheTTLHours, 24)
	}
	if cfg.CacheCleanupIntervalMin != 10 {
		t.Errorf("CacheCleanupIntervalMin: got %d, want %d", cfg.CacheCleanupIntervalMin, 10)
	}
	if cfg.MaxConcurrentJobs != 1 {
		t.Errorf("MaxConcurrentJobs: got %d, want %d", cfg.MaxConcurrentJobs, 1)
	}
	if cfg.AllowedOrigins != "http://localhost:5173" {
		t.Errorf("AllowedOrigins: got %q, want %q", cfg.AllowedOrigins, "http://localhost:5173")
	}
	if cfg.Port != 8080 {
		t.Errorf("Port: got %d, want %d", cfg.Port, 8080)
	}
	if cfg.VideoIDSigningKey != strings.Repeat("a", 32) {
		t.Errorf("VideoIDSigningKey: got %q, want %q", cfg.VideoIDSigningKey, strings.Repeat("a", 32))
	}
}

// TestLoad_HappyPath_AllExplicit verifies that when every env var is set to a
// non-default value the returned Config reflects those overrides exactly.
func TestLoad_HappyPath_AllExplicit(t *testing.T) {
	signingKey := strings.Repeat("z", 64)

	t.Setenv("VIDEO_ID_SIGNING_KEY", signingKey)
	t.Setenv("PYTHON_PROCESSOR_URL", "http://python:9999")
	t.Setenv("AUDIO_TMP_DIR", "/var/audio/tmp")
	t.Setenv("CACHE_DIR", "/var/audio/cache")
	t.Setenv("CACHE_TTL_HOURS", "48")
	t.Setenv("CACHE_CLEANUP_INTERVAL_MIN", "30")
	t.Setenv("MAX_CONCURRENT_JOBS", "5")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")
	t.Setenv("PORT", "9090")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.PythonProcessorURL != "http://python:9999" {
		t.Errorf("PythonProcessorURL: got %q, want %q", cfg.PythonProcessorURL, "http://python:9999")
	}
	if cfg.AudioTmpDir != "/var/audio/tmp" {
		t.Errorf("AudioTmpDir: got %q, want %q", cfg.AudioTmpDir, "/var/audio/tmp")
	}
	if cfg.CacheDir != "/var/audio/cache" {
		t.Errorf("CacheDir: got %q, want %q", cfg.CacheDir, "/var/audio/cache")
	}
	if cfg.CacheTTLHours != 48 {
		t.Errorf("CacheTTLHours: got %d, want %d", cfg.CacheTTLHours, 48)
	}
	if cfg.CacheCleanupIntervalMin != 30 {
		t.Errorf("CacheCleanupIntervalMin: got %d, want %d", cfg.CacheCleanupIntervalMin, 30)
	}
	if cfg.MaxConcurrentJobs != 5 {
		t.Errorf("MaxConcurrentJobs: got %d, want %d", cfg.MaxConcurrentJobs, 5)
	}
	if cfg.AllowedOrigins != "https://example.com" {
		t.Errorf("AllowedOrigins: got %q, want %q", cfg.AllowedOrigins, "https://example.com")
	}
	if cfg.Port != 9090 {
		t.Errorf("Port: got %d, want %d", cfg.Port, 9090)
	}
	if cfg.VideoIDSigningKey != signingKey {
		t.Errorf("VideoIDSigningKey: got %q, want %q", cfg.VideoIDSigningKey, signingKey)
	}
}

// TestLoad_ErrorCases is a table-driven test covering all validation failures.
func TestLoad_ErrorCases(t *testing.T) {
	validKey := strings.Repeat("a", 32)

	tests := []struct {
		name       string
		setup      func(t *testing.T)
		wantErrSub string
	}{
		{
			name: "missing VIDEO_ID_SIGNING_KEY",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", "")
			},
			wantErrSub: "VIDEO_ID_SIGNING_KEY",
		},
		{
			name: "VIDEO_ID_SIGNING_KEY too short",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", "tooshort")
			},
			wantErrSub: "VIDEO_ID_SIGNING_KEY",
		},
		{
			name: "VIDEO_ID_SIGNING_KEY exactly 31 chars (one below minimum)",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", strings.Repeat("b", 31))
			},
			wantErrSub: "VIDEO_ID_SIGNING_KEY",
		},
		{
			name: "invalid PORT",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", validKey)
				t.Setenv("PORT", "not-a-number")
			},
			wantErrSub: "PORT",
		},
		{
			name: "invalid CACHE_TTL_HOURS",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", validKey)
				t.Setenv("CACHE_TTL_HOURS", "banana")
			},
			wantErrSub: "CACHE_TTL_HOURS",
		},
		{
			name: "invalid CACHE_CLEANUP_INTERVAL_MIN",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", validKey)
				t.Setenv("CACHE_CLEANUP_INTERVAL_MIN", "??")
			},
			wantErrSub: "CACHE_CLEANUP_INTERVAL_MIN",
		},
		{
			name: "invalid MAX_CONCURRENT_JOBS",
			setup: func(t *testing.T) {
				t.Setenv("VIDEO_ID_SIGNING_KEY", validKey)
				t.Setenv("MAX_CONCURRENT_JOBS", "zero")
			},
			wantErrSub: "MAX_CONCURRENT_JOBS",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() returned nil error; want error containing %q", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}
