package config

import (
	"fmt"
	"os"
	"strconv"
)

const minSigningKeyLen = 32

// Config holds all runtime configuration for the backend service.
// Values are read from environment variables; missing/empty vars fall back
// to the documented defaults.
type Config struct {
	PythonProcessorURL string // PYTHON_PROCESSOR_URL, default "http://localhost:8090"
	AudioTmpDir        string // AUDIO_TMP_DIR, default "./tmp"
	CacheDir           string // CACHE_DIR, default "./tmp/cache"
	MaxConcurrentJobs  int    // MAX_CONCURRENT_JOBS, default 1
	AllowedOrigins     string // ALLOWED_ORIGINS, default "http://localhost:5173"
	Port               int    // PORT, default 8080
	VideoIDSigningKey  string // VIDEO_ID_SIGNING_KEY, required, >= 32 chars
	LogLevel           string // LOG_LEVEL, one of debug/info/warn/error, default "info"
}

// Load reads environment variables and returns a validated Config.
// Returns an error if VIDEO_ID_SIGNING_KEY is missing or shorter than 32
// characters, LOG_LEVEL is not one of debug/info/warn/error, or if any
// integer-typed variable cannot be parsed.
func Load() (*Config, error) {
	cfg := &Config{}

	// LOG_LEVEL: optional, strict allowlist, default "info".
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	switch logLevel {
	case "debug", "info", "warn", "error":
		// valid
	default:
		return nil, fmt.Errorf("LOG_LEVEL: %q is not one of debug/info/warn/error", logLevel)
	}
	cfg.LogLevel = logLevel

	// String fields with defaults.
	cfg.PythonProcessorURL = getEnvString("PYTHON_PROCESSOR_URL", "http://localhost:8090")
	cfg.AudioTmpDir = getEnvString("AUDIO_TMP_DIR", "./tmp")
	cfg.CacheDir = getEnvString("CACHE_DIR", "./tmp/cache")
	cfg.AllowedOrigins = getEnvString("ALLOWED_ORIGINS", "http://localhost:5173")

	// Integer fields with defaults.
	var err error
	if cfg.MaxConcurrentJobs, err = getEnvInt("MAX_CONCURRENT_JOBS", 1); err != nil {
		return nil, err
	}
	if cfg.Port, err = getEnvInt("PORT", 8080); err != nil {
		return nil, err
	}

	// Required: VIDEO_ID_SIGNING_KEY must be present and >= 32 chars.
	key := os.Getenv("VIDEO_ID_SIGNING_KEY")
	if len(key) < minSigningKeyLen {
		return nil, fmt.Errorf("VIDEO_ID_SIGNING_KEY must be at least %d characters, got %d", minSigningKeyLen, len(key))
	}
	cfg.VideoIDSigningKey = key

	return cfg, nil
}

// getEnvString returns the value of the named environment variable, or def if
// the variable is unset or empty.
func getEnvString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvInt returns the integer value of the named environment variable, or def
// if the variable is unset or empty. Returns an error (containing the env var
// name) if the value cannot be parsed as an integer.
func getEnvInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
