package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CPUProcessorClient covers Python work that does not require a GPU.
type CPUProcessorClient interface {
	Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error
	PreviewKey(ctx context.Context, inputURL string) (string, error)
}

// GPUProcessorClient covers Python work that needs a GPU.
type GPUProcessorClient interface {
	Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
	Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}

// PythonCPUProcessorClient is the HTTP-backed CPUProcessorClient.
type PythonCPUProcessorClient struct {
	baseURL string
	client  *http.Client
}

// NewPythonCPUProcessorClient returns a CPU client. http.Client.Timeout governs
// the overall request budget (~30s in production).
func NewPythonCPUProcessorClient(baseURL string, client *http.Client) *PythonCPUProcessorClient {
	return &PythonCPUProcessorClient{baseURL: baseURL, client: client}
}

// PythonGPUProcessorClient is the HTTP-backed GPUProcessorClient.
type PythonGPUProcessorClient struct {
	baseURL string
	client  *http.Client
}

// NewPythonGPUProcessorClient returns a GPU client. http.Client.Timeout should
// be large enough to absorb HF Space cold start + Demucs runtime (~180s).
func NewPythonGPUProcessorClient(baseURL string, client *http.Client) *PythonGPUProcessorClient {
	return &PythonGPUProcessorClient{baseURL: baseURL, client: client}
}

// postJSON marshals body, POSTs to baseURL+path, and decodes the response into
// out (which may be nil to discard). 2xx = success.
func postJSON(ctx context.Context, client *http.Client, baseURL, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("processor %s: marshal: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("processor %s: build request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("processor %s: do: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("processor %s: upstream status %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("processor %s: decode: %w", path, err)
	}
	return nil
}

// Shift calls the Python /shift endpoint to pitch-shift audio at inputURL by
// semitones, writing the result to outputURL.
func (p *PythonCPUProcessorClient) Shift(ctx context.Context, inputURL, outputURL string, semitones float64) error {
	return postJSON(ctx, p.client, p.baseURL, "/shift", map[string]any{
		"input_url":  inputURL,
		"output_url": outputURL,
		"semitones":  semitones,
	}, nil)
}

// PreviewKey calls the Python /preview-key endpoint to estimate the musical key
// of the audio at inputURL. Returns the key string (e.g. "A major") or "" for
// silent input.
func (p *PythonCPUProcessorClient) PreviewKey(ctx context.Context, inputURL string) (string, error) {
	var resp struct {
		Key string `json:"key"`
	}
	if err := postJSON(ctx, p.client, p.baseURL, "/preview-key", map[string]any{
		"input_url": inputURL,
	}, &resp); err != nil {
		return "", err
	}
	return resp.Key, nil
}

// Separate calls the Python /separate endpoint to split the audio at inputURL
// into vocals and no-vocals stems, uploading results to vocalsOutputURL and
// noVocalsOutputURL respectively.
func (p *PythonGPUProcessorClient) Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error {
	return postJSON(ctx, p.client, p.baseURL, "/separate", map[string]any{
		"input_url":            inputURL,
		"vocals_output_url":    vocalsOutputURL,
		"no_vocals_output_url": noVocalsOutputURL,
	}, nil)
}

// Melody calls the Python /melody endpoint to extract pitch data from the
// vocals stem at vocalsInputURL, uploading the result JSON to outputURL.
func (p *PythonGPUProcessorClient) Melody(ctx context.Context, vocalsInputURL, outputURL string) error {
	return postJSON(ctx, p.client, p.baseURL, "/melody", map[string]any{
		"vocals_input_url": vocalsInputURL,
		"output_url":       outputURL,
	}, nil)
}
