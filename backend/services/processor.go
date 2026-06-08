package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ProcessorClient abstracts the audio-processor HTTP service.
type ProcessorClient interface {
	Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
	Separate(ctx context.Context, inputPath, outputDir string) (vocalsPath, noVocalsPath string, err error)
	Melody(ctx context.Context, vocalsPath, outputPath string) error
}

// separateResponse is the JSON body returned by the Python /separate endpoint.
type separateResponse struct {
	VocalsPath   string `json:"vocals_path"`
	NoVocalsPath string `json:"no_vocals_path"`
}

// PythonProcessorClient is the concrete implementation backed by the FastAPI service.
type PythonProcessorClient struct {
	baseURL string
	client  *http.Client
}

// NewPythonProcessorClient returns a PythonProcessorClient configured with the given base URL and HTTP client.
func NewPythonProcessorClient(baseURL string, client *http.Client) *PythonProcessorClient {
	return &PythonProcessorClient{
		baseURL: baseURL,
		client:  client,
	}
}

// Shift calls the Python /shift endpoint to pitch-shift the audio at inputPath by semitones,
// writing the result to outputPath.
func (p *PythonProcessorClient) Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("processor shift: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"input_path":  inputPath,
		"output_path": outputPath,
		"semitones":   semitones,
	})
	if err != nil {
		return fmt.Errorf("processor shift: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/shift", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("processor shift: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("processor shift: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("processor shift: upstream status %d", resp.StatusCode)
	}

	return nil
}

// Separate calls the Python /separate endpoint to split inputPath into vocals and no-vocals stems
// written under outputDir, returning the two output paths.
func (p *PythonProcessorClient) Separate(ctx context.Context, inputPath, outputDir string) (vocalsPath, noVocalsPath string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("processor separate: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"input_path": inputPath,
		"output_dir": outputDir,
	})
	if err != nil {
		return "", "", fmt.Errorf("processor separate: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/separate", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("processor separate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("processor separate: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("processor separate: upstream status %d", resp.StatusCode)
	}

	var result separateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("processor separate: decode response: %w", err)
	}

	return result.VocalsPath, result.NoVocalsPath, nil
}

// Melody calls the Python /melody endpoint to extract pitch data from vocalsPath and write it to outputPath.
func (p *PythonProcessorClient) Melody(ctx context.Context, vocalsPath, outputPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("processor melody: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"vocals_path": vocalsPath,
		"output_path": outputPath,
	})
	if err != nil {
		return fmt.Errorf("processor melody: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/melody", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("processor melody: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("processor melody: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("processor melody: upstream status %d", resp.StatusCode)
	}

	return nil
}
