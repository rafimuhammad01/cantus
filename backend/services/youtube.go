package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"cantus/backend/models"
)

// CommandRunner abstracts external command execution for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// ExecRunner runs commands using os/exec.
type ExecRunner struct{}

// Run executes name with args under ctx using os/exec.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// YouTubeService abstracts song search and audio retrieval.
type YouTubeService interface {
	Search(ctx context.Context, query string, limit, offset int) (SearchPage, error)
	DownloadPreview(ctx context.Context, videoID string) error
	DownloadFull(ctx context.Context, videoID string) error
}

// SearchPage holds a page of search results and a continuation flag.
type SearchPage struct {
	Items   []models.SearchResult
	HasMore bool
}

// PythonYouTubeService proxies search to the FastAPI audio-processor and HMAC-signs results.
type PythonYouTubeService struct {
	baseURL string
	client  *http.Client
	signer  *Signer
	storage Storage
	runner  CommandRunner
}

// NewPythonYouTubeService returns a PythonYouTubeService configured with the given base URL, HTTP client, signer, storage, and command runner.
func NewPythonYouTubeService(baseURL string, client *http.Client, signer *Signer, storage Storage, runner CommandRunner) *PythonYouTubeService {
	return &PythonYouTubeService{
		baseURL: baseURL,
		client:  client,
		signer:  signer,
		storage: storage,
		runner:  runner,
	}
}

// upstreamItem is the JSON shape returned by the Python /search endpoint per item.
type upstreamItem struct {
	VideoID      string  `json:"videoId"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        *string `json:"album"`
	DurationSec  int     `json:"duration_sec"`
	ThumbnailURL string  `json:"thumbnail_url"`
}

// upstreamResponse is the top-level JSON shape returned by the Python /search endpoint.
type upstreamResponse struct {
	Items   []upstreamItem `json:"items"`
	HasMore bool           `json:"has_more"`
}

// Search calls the Python /search endpoint and returns HMAC-signed results, dropping invalid video IDs.
func (s *PythonYouTubeService) Search(ctx context.Context, query string, limit, offset int) (SearchPage, error) {
	if err := ctx.Err(); err != nil {
		return SearchPage{}, fmt.Errorf("youtube search: %w", err)
	}

	reqBody, err := json.Marshal(map[string]any{
		"query":  query,
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return SearchPage{}, fmt.Errorf("youtube search: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/search", bytes.NewReader(reqBody))
	if err != nil {
		return SearchPage{}, fmt.Errorf("youtube search: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return SearchPage{}, fmt.Errorf("youtube search: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchPage{}, fmt.Errorf("youtube search: upstream status %d", resp.StatusCode)
	}

	var upstream upstreamResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return SearchPage{}, fmt.Errorf("youtube search: decode response: %w", err)
	}

	mapped := make([]models.SearchResult, 0, len(upstream.Items))
	for _, item := range upstream.Items {
		if !ValidVideoID(item.VideoID) {
			continue
		}
		album := ""
		if item.Album != nil {
			album = *item.Album
		}
		mapped = append(mapped, models.SearchResult{
			VideoID:      item.VideoID,
			Sig:          s.signer.Sign(item.VideoID),
			Title:        item.Title,
			Artist:       item.Artist,
			Album:        album,
			DurationSec:  item.DurationSec,
			ThumbnailURL: item.ThumbnailURL,
		})
	}

	return SearchPage{Items: mapped, HasMore: upstream.HasMore}, nil
}

// DownloadPreview downloads the first 30 seconds of audio for videoID via yt-dlp
// and commits it to storage under the name "preview.mp3". It returns an error if
// videoID is invalid, yt-dlp fails, or storage.Commit fails.
func (s *PythonYouTubeService) DownloadPreview(ctx context.Context, videoID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download preview: %w", err)
	}

	if !ValidVideoID(videoID) {
		return fmt.Errorf("download preview: invalid videoID %q", videoID)
	}

	tmpDir, err := os.MkdirTemp("", "cantus-preview-*")
	if err != nil {
		return fmt.Errorf("download preview: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	outPath := filepath.Join(tmpDir, "preview.mp3")

	args := []string{
		"--download-sections", "*0-30",
		"-x", "--audio-format", "mp3",
		"-o", outPath,
		"--quiet", "--no-warnings",
		"--", // guards against videoIDs that could be interpreted as flags
		"https://youtu.be/" + videoID,
	}

	if err := s.runner.Run(ctx, "yt-dlp", args...); err != nil {
		return fmt.Errorf("download preview: yt-dlp: %w", err)
	}

	if err := s.storage.Commit(ctx, s.storage.Key(videoID, "preview.mp3"), outPath); err != nil {
		return fmt.Errorf("download preview: commit: %w", err)
	}

	return nil
}

// DownloadFull downloads the full audio for videoID via yt-dlp and commits it
// to storage under the name "original.wav". It returns an error if videoID is
// invalid, yt-dlp fails, or storage.Commit fails.
func (s *PythonYouTubeService) DownloadFull(ctx context.Context, videoID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("download full: %w", err)
	}

	if !ValidVideoID(videoID) {
		return fmt.Errorf("download full: invalid videoID %q", videoID)
	}

	tmpDir, err := os.MkdirTemp("", "cantus-full-*")
	if err != nil {
		return fmt.Errorf("download full: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	outPath := filepath.Join(tmpDir, "original.wav")

	args := []string{
		"-x", "--audio-format", "wav",
		"-o", outPath,
		"--quiet", "--no-warnings",
		"--", // guards against videoIDs that could be interpreted as flags
		"https://youtu.be/" + videoID,
	}

	if err := s.runner.Run(ctx, "yt-dlp", args...); err != nil {
		return fmt.Errorf("download full: yt-dlp: %w", err)
	}

	if err := s.storage.Commit(ctx, s.storage.Key(videoID, "original.wav"), outPath); err != nil {
		return fmt.Errorf("download full: commit: %w", err)
	}

	return nil
}
