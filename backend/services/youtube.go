package services

import (
	"context"
	"fmt"
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

// Searcher is implemented by YTMusicSearch and any other Search backend.
type Searcher interface {
	Search(ctx context.Context, query string, limit, offset int) (SearchPage, error)
}

// PythonYouTubeService handles audio download via yt-dlp and delegates search to a Searcher.
type PythonYouTubeService struct {
	search  Searcher
	signer  *Signer
	storage Storage
	runner  CommandRunner
}

// NewPythonYouTubeService returns a PythonYouTubeService with the given search delegate, signer, storage, and command runner.
func NewPythonYouTubeService(search Searcher, signer *Signer, storage Storage, runner CommandRunner) *PythonYouTubeService {
	return &PythonYouTubeService{search: search, signer: signer, storage: storage, runner: runner}
}

// Search delegates to the injected Searcher.
func (s *PythonYouTubeService) Search(ctx context.Context, query string, limit, offset int) (SearchPage, error) {
	return s.search.Search(ctx, query, limit, offset)
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
