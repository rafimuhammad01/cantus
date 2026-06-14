package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cantus/backend/services"
)

func TestPythonProcessorClient_Separate(t *testing.T) {
	tests := []struct {
		name              string
		inputURL          string
		vocalsOutputURL   string
		noVocalsOutputURL string
		handler           http.HandlerFunc
		wantErr           bool
		errContains       string
	}{
		{
			name:              "happy path sends all three URLs",
			inputURL:          "https://r2/in.mp3",
			vocalsOutputURL:   "https://r2/v.wav",
			noVocalsOutputURL: "https://r2/nv.wav",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/separate" {
					t.Errorf("path: got %q, want /separate", r.URL.Path)
				}
				var body struct {
					InputURL          string `json:"input_url"`
					VocalsOutputURL   string `json:"vocals_output_url"`
					NoVocalsOutputURL string `json:"no_vocals_output_url"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				if body.InputURL != "https://r2/in.mp3" {
					t.Errorf("input_url: got %q", body.InputURL)
				}
				if body.VocalsOutputURL != "https://r2/v.wav" {
					t.Errorf("vocals_output_url: got %q", body.VocalsOutputURL)
				}
				if body.NoVocalsOutputURL != "https://r2/nv.wav" {
					t.Errorf("no_vocals_output_url: got %q", body.NoVocalsOutputURL)
				}
				w.WriteHeader(http.StatusNoContent)
			},
		},
		{
			name:              "upstream 500 returns error",
			inputURL:          "https://r2/in.mp3",
			vocalsOutputURL:   "https://r2/v.wav",
			noVocalsOutputURL: "https://r2/nv.wav",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "demucs failed", http.StatusInternalServerError)
			},
			wantErr:     true,
			errContains: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := services.NewPythonProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
			err := c.Separate(context.Background(), tt.inputURL, tt.vocalsOutputURL, tt.noVocalsOutputURL)

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
		})
	}
}

func TestPythonProcessorClient_Melody(t *testing.T) {
	tests := []struct {
		name           string
		vocalsInputURL string
		outputURL      string
		handler        http.HandlerFunc
		wantErr        bool
		errContains    string
	}{
		{
			name:           "happy path sends vocals_input_url and output_url",
			vocalsInputURL: "https://r2/v.wav",
			outputURL:      "https://r2/m.json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/melody" {
					t.Errorf("path: got %q, want /melody", r.URL.Path)
				}
				var body struct {
					VocalsInputURL string `json:"vocals_input_url"`
					OutputURL      string `json:"output_url"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
				}
				if body.VocalsInputURL != "https://r2/v.wav" {
					t.Errorf("vocals_input_url: got %q", body.VocalsInputURL)
				}
				if body.OutputURL != "https://r2/m.json" {
					t.Errorf("output_url: got %q", body.OutputURL)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			},
		},
		{
			name:           "upstream 502 returns error",
			vocalsInputURL: "https://r2/v.wav",
			outputURL:      "https://r2/m.json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "crepe failed", http.StatusBadGateway)
			},
			wantErr:     true,
			errContains: "502",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := services.NewPythonProcessorClient(srv.URL, &http.Client{Timeout: 5 * time.Second})
			err := c.Melody(context.Background(), tt.vocalsInputURL, tt.outputURL)

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
