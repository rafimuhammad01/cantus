package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"cantus/backend/services"
)

func TestPythonProcessorClient_Shift(t *testing.T) {
	tests := []struct {
		name        string
		inputPath   string
		outputPath  string
		semitones   float64
		transport   roundTripperFunc
		wantErr     bool
		errContains string
	}{
		{
			name:       "happy path semitones zero",
			inputPath:  "/audio/a.mp3",
			outputPath: "/audio/a_out.mp3",
			semitones:  0.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("method: got %q, want POST", r.Method)
				}
				if r.URL.Path != "/shift" {
					t.Errorf("path: got %q, want /shift", r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type: got %q, want application/json", ct)
				}
				var body struct {
					InputPath  string  `json:"input_path"`
					OutputPath string  `json:"output_path"`
					Semitones  float64 `json:"semitones"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.InputPath != "/audio/a.mp3" {
					t.Errorf("input_path: got %q, want %q", body.InputPath, "/audio/a.mp3")
				}
				if body.OutputPath != "/audio/a_out.mp3" {
					t.Errorf("output_path: got %q, want %q", body.OutputPath, "/audio/a_out.mp3")
				}
				if body.Semitones != 0.0 {
					t.Errorf("semitones: got %v, want 0.0", body.Semitones)
				}
				return makeResponse(http.StatusOK, `{"output_path":"/audio/a_out.mp3"}`), nil
			}),
			wantErr: false,
		},
		{
			name:       "happy path semitones negative",
			inputPath:  "/tracks/song.mp3",
			outputPath: "/tracks/song_shift.mp3",
			semitones:  -2.5,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				var body struct {
					InputPath  string  `json:"input_path"`
					OutputPath string  `json:"output_path"`
					Semitones  float64 `json:"semitones"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.InputPath != "/tracks/song.mp3" {
					t.Errorf("input_path: got %q, want %q", body.InputPath, "/tracks/song.mp3")
				}
				if body.Semitones != -2.5 {
					t.Errorf("semitones: got %v, want -2.5", body.Semitones)
				}
				return makeResponse(http.StatusOK, `{"output_path":"/tracks/song_shift.mp3"}`), nil
			}),
			wantErr: false,
		},
		{
			name:       "happy path semitones positive",
			inputPath:  "/data/in.mp3",
			outputPath: "/data/out.mp3",
			semitones:  3.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				var body struct {
					InputPath  string  `json:"input_path"`
					OutputPath string  `json:"output_path"`
					Semitones  float64 `json:"semitones"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.Semitones != 3.0 {
					t.Errorf("semitones: got %v, want 3.0", body.Semitones)
				}
				return makeResponse(http.StatusOK, `{"output_path":"/data/out.mp3"}`), nil
			}),
			wantErr: false,
		},
		{
			name:       "upstream 404 input missing",
			inputPath:  "/missing/x.mp3",
			outputPath: "/missing/x_out.mp3",
			semitones:  -1.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusNotFound, `{"detail":"input_path not found"}`), nil
			}),
			wantErr:     true,
			errContains: "404",
		},
		{
			name:       "upstream 500 processing failure",
			inputPath:  "/tmp/b.mp3",
			outputPath: "/tmp/b_out.mp3",
			semitones:  2.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusInternalServerError, `{"detail":"ffmpeg failed: exit status 1"}`), nil
			}),
			wantErr:     true,
			errContains: "500",
		},
		{
			name:       "upstream 422 validation error",
			inputPath:  "/tmp/c.mp3",
			outputPath: "/tmp/c_out.mp3",
			semitones:  99.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusUnprocessableEntity, `{"detail":[{"msg":"semitones out of range"}]}`), nil
			}),
			wantErr:     true,
			errContains: "422",
		},
		{
			name:       "malformed JSON response 200 no error",
			inputPath:  "/tmp/d.mp3",
			outputPath: "/tmp/d_out.mp3",
			semitones:  0.5,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, "not json"), nil
			}),
			wantErr: false,
		},
		{
			name:       "network error",
			inputPath:  "/tmp/e.mp3",
			outputPath: "/tmp/e_out.mp3",
			semitones:  1.0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return nil, errors.New("net down")
			}),
			wantErr:     true,
			errContains: "net down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			err := proc.Shift(context.Background(), tt.inputPath, tt.outputPath, tt.semitones)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Shift: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("Shift: unexpected error: %v", err)
			}
		})
	}
}

func TestPythonProcessorClient_Separate(t *testing.T) {
	tests := []struct {
		name         string
		inputPath    string
		outputDir    string
		transport    roundTripperFunc
		wantErr      bool
		errContains  string
		wantVocals   string
		wantNoVocals string
	}{
		{
			name:      "happy path returns two paths",
			inputPath: "/audio/full.mp3",
			outputDir: "/audio/stems",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("method: got %q, want POST", r.Method)
				}
				if r.URL.Path != "/separate" {
					t.Errorf("path: got %q, want /separate", r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type: got %q, want application/json", ct)
				}
				var body struct {
					InputPath string `json:"input_path"`
					OutputDir string `json:"output_dir"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.InputPath != "/audio/full.mp3" {
					t.Errorf("input_path: got %q, want %q", body.InputPath, "/audio/full.mp3")
				}
				if body.OutputDir != "/audio/stems" {
					t.Errorf("output_dir: got %q, want %q", body.OutputDir, "/audio/stems")
				}
				return makeResponse(http.StatusOK, `{"vocals_path":"/audio/stems/vocals.wav","no_vocals_path":"/audio/stems/no_vocals.wav"}`), nil
			}),
			wantErr:      false,
			wantVocals:   "/audio/stems/vocals.wav",
			wantNoVocals: "/audio/stems/no_vocals.wav",
		},
		{
			name:      "upstream 404 input missing",
			inputPath: "/missing/full.mp3",
			outputDir: "/missing/stems",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusNotFound, `{"detail":"input_path not found"}`), nil
			}),
			wantErr:     true,
			errContains: "404",
		},
		{
			name:      "upstream 500 processing failure",
			inputPath: "/tmp/full.mp3",
			outputDir: "/tmp/stems",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusInternalServerError, `{"detail":"demucs failed"}`), nil
			}),
			wantErr:     true,
			errContains: "500",
		},
		{
			name:      "malformed JSON in 2xx response",
			inputPath: "/tmp/f.mp3",
			outputDir: "/tmp/stems",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, "not json"), nil
			}),
			wantErr: true,
		},
		{
			name:      "network error",
			inputPath: "/tmp/g.mp3",
			outputDir: "/tmp/stems",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return nil, errors.New("net down")
			}),
			wantErr:     true,
			errContains: "net down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			vocals, noVocals, err := proc.Separate(context.Background(), tt.inputPath, tt.outputDir)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Separate: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("Separate: unexpected error: %v", err)
			}
			if vocals != tt.wantVocals {
				t.Errorf("vocalsPath: got %q, want %q", vocals, tt.wantVocals)
			}
			if noVocals != tt.wantNoVocals {
				t.Errorf("noVocalsPath: got %q, want %q", noVocals, tt.wantNoVocals)
			}
		})
	}
}

func TestPythonProcessorClient_Separate_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns context.Canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, `{"vocals_path":"/v.wav","no_vocals_path":"/nv.wav"}`), nil
			})
			client := &http.Client{Transport: transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, _, err := proc.Separate(ctx, "/x.mp3", "/stems")
			if err == nil {
				t.Fatalf("Separate with canceled ctx: got nil error, want error")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error %v: expected errors.Is(err, context.Canceled) to be true", err)
			}
		})
	}
}

func TestPythonProcessorClient_Melody(t *testing.T) {
	tests := []struct {
		name        string
		vocalsPath  string
		outputPath  string
		transport   roundTripperFunc
		wantErr     bool
		errContains string
	}{
		{
			name:       "happy path 200 no error",
			vocalsPath: "/stems/vocals.wav",
			outputPath: "/out/melody.json",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("method: got %q, want POST", r.Method)
				}
				if r.URL.Path != "/melody" {
					t.Errorf("path: got %q, want /melody", r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type: got %q, want application/json", ct)
				}
				var body struct {
					VocalsPath string `json:"vocals_path"`
					OutputPath string `json:"output_path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.VocalsPath != "/stems/vocals.wav" {
					t.Errorf("vocals_path: got %q, want %q", body.VocalsPath, "/stems/vocals.wav")
				}
				if body.OutputPath != "/out/melody.json" {
					t.Errorf("output_path: got %q, want %q", body.OutputPath, "/out/melody.json")
				}
				return makeResponse(http.StatusOK, `{"output_path":"/out/melody.json"}`), nil
			}),
			wantErr: false,
		},
		{
			name:       "upstream 404 vocals missing",
			vocalsPath: "/missing/vocals.wav",
			outputPath: "/out/melody.json",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusNotFound, `{"detail":"vocals_path not found"}`), nil
			}),
			wantErr:     true,
			errContains: "404",
		},
		{
			name:       "upstream 500 processing failure",
			vocalsPath: "/tmp/vocals.wav",
			outputPath: "/tmp/melody.json",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusInternalServerError, `{"detail":"crepe failed"}`), nil
			}),
			wantErr:     true,
			errContains: "500",
		},
		{
			name:       "malformed JSON in 2xx no error",
			vocalsPath: "/tmp/vocals.wav",
			outputPath: "/tmp/melody.json",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, "not json"), nil
			}),
			wantErr: false,
		},
		{
			name:       "network error",
			vocalsPath: "/tmp/vocals.wav",
			outputPath: "/tmp/melody.json",
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return nil, errors.New("net down")
			}),
			wantErr:     true,
			errContains: "net down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			err := proc.Melody(context.Background(), tt.vocalsPath, tt.outputPath)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Melody: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("Melody: unexpected error: %v", err)
			}
		})
	}
}

func TestPythonProcessorClient_Melody_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns context.Canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, `{"output_path":"/out/melody.json"}`), nil
			})
			client := &http.Client{Transport: transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := proc.Melody(ctx, "/stems/vocals.wav", "/out/melody.json")
			if err == nil {
				t.Fatalf("Melody with canceled ctx: got nil error, want error")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error %v: expected errors.Is(err, context.Canceled) to be true", err)
			}
		})
	}
}

func TestPythonProcessorClient_Shift_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns context.Canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, `{"output_path":"/x.mp3"}`), nil
			})
			client := &http.Client{Transport: transport}
			proc := services.NewPythonProcessorClient("http://localhost:8090", client)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := proc.Shift(ctx, "/x.mp3", "/x_out.mp3", 0.0)
			if err == nil {
				t.Fatalf("Shift with canceled ctx: got nil error, want error")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error %v: expected errors.Is(err, context.Canceled) to be true", err)
			}
		})
	}
}
