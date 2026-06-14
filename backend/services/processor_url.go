package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ProcessorClient covers GPU-bound Python work: Demucs vocal separation
// and CREPE melody extraction. The Python service is reached over HTTP with
// presigned URL handoff for audio data.
type ProcessorClient interface {
	Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error
	Melody(ctx context.Context, vocalsInputURL, outputURL string) error
}

// PythonProcessorClient is the HTTP-backed ProcessorClient.
type PythonProcessorClient struct {
	baseURL string
	client  *http.Client
}

// NewPythonProcessorClient returns a processor client. http.Client.Timeout should
// be large enough to absorb HF Space cold start + Demucs runtime (~180s).
func NewPythonProcessorClient(baseURL string, client *http.Client) *PythonProcessorClient {
	return &PythonProcessorClient{baseURL: baseURL, client: client}
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

// Separate calls the Python /separate endpoint to split the audio at inputURL
// into vocals and no-vocals stems, uploading results to vocalsOutputURL and
// noVocalsOutputURL respectively.
func (p *PythonProcessorClient) Separate(ctx context.Context, inputURL, vocalsOutputURL, noVocalsOutputURL string) error {
	return postJSON(ctx, p.client, p.baseURL, "/separate", map[string]any{
		"input_url":            inputURL,
		"vocals_output_url":    vocalsOutputURL,
		"no_vocals_output_url": noVocalsOutputURL,
	}, nil)
}

// Melody calls the Python /melody endpoint to extract pitch data from the
// vocals stem at vocalsInputURL, uploading the result JSON to outputURL.
func (p *PythonProcessorClient) Melody(ctx context.Context, vocalsInputURL, outputURL string) error {
	return postJSON(ctx, p.client, p.baseURL, "/melody", map[string]any{
		"vocals_input_url": vocalsInputURL,
		"output_url":       outputURL,
	}, nil)
}
