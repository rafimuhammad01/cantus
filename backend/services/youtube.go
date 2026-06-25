package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"cantus/backend/models"
)

// DownloadStallTimeout is the maximum duration without any new bytes written
// to the output file before the download is considered stalled and cancelled.
// The existing Retry wrapper will then retry the cancelled attempt.
const DownloadStallTimeout = 30 * time.Second

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
	search      Searcher
	signer      *Signer
	storage     Storage
	runner      CommandRunner
	cookiesPath string
}

// NewPythonYouTubeService returns a PythonYouTubeService with the given search delegate, signer, storage, command runner, and optional cookies file path.
func NewPythonYouTubeService(search Searcher, signer *Signer, storage Storage, runner CommandRunner, cookiesPath string) *PythonYouTubeService {
	return &PythonYouTubeService{search: search, signer: signer, storage: storage, runner: runner, cookiesPath: cookiesPath}
}

// botGateArgs returns yt-dlp args needed to pass YouTube's auth + JS challenge
// gates. EJS solver scripts are required by yt-dlp 2026.06.09+ to solve signature
// and n-challenges; without them only image storyboards are served.
func (s *PythonYouTubeService) botGateArgs() []string {
	args := []string{"--remote-components", "ejs:github"}
	if s.cookiesPath != "" {
		args = append(args, "--cookies", s.cookiesPath)
	}
	return args
}

// Search delegates to the injected Searcher.
func (s *PythonYouTubeService) Search(ctx context.Context, query string, limit, offset int) (SearchPage, error) {
	return s.search.Search(ctx, query, limit, offset)
}

// runWithStallDetector runs the command via s.runner but cancels outCtx if outPath
// shows no growth for DownloadStallTimeout. This prevents silent hangs on slow or
// throttled connections; the Retry wrapper in the caller will retry the cancelled attempt.
func (s *PythonYouTubeService) runWithStallDetector(ctx context.Context, outPath, name string, args ...string) error {
	stallCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- s.runner.Run(stallCtx, name, args...)
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastSize int64 = -1
	var lastChange = time.Now()

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			info, statErr := os.Stat(outPath)
			if statErr != nil {
				// File not created yet — reset the stall clock as long as the
				// command is still starting up (within DownloadStallTimeout).
				if time.Since(lastChange) >= DownloadStallTimeout {
					cancel()
					return fmt.Errorf("download stalled: no output file after %s", DownloadStallTimeout)
				}
				continue
			}
			size := info.Size()
			if size != lastSize {
				lastSize = size
				lastChange = time.Now()
			} else if time.Since(lastChange) >= DownloadStallTimeout {
				cancel()
				return fmt.Errorf("download stalled: no progress for %s", DownloadStallTimeout)
			}
		}
	}
}

// DownloadPreview downloads the first 30 seconds of audio for videoID via yt-dlp
// and commits it to storage under the name "preview.wav". It returns an error if
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

	outPath := filepath.Join(tmpDir, "preview.wav")

	args := append(s.botGateArgs(), []string{
		"--download-sections", "*0-30",
		"-x", "--audio-format", "wav",
		"-o", outPath,
		"--quiet", "--no-warnings",
		"--", // guards against videoIDs that could be interpreted as flags
		"https://youtu.be/" + videoID,
	}...)

	if err := s.runWithStallDetector(ctx, outPath, "yt-dlp", args...); err != nil {
		return fmt.Errorf("download preview: yt-dlp: %w", err)
	}

	if err := s.storage.Commit(ctx, s.storage.Key(videoID, "preview.wav"), outPath); err != nil {
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

	args := append(s.botGateArgs(), []string{
		"-x", "--audio-format", "wav",
		"-o", outPath,
		"--quiet", "--no-warnings",
		"--", // guards against videoIDs that could be interpreted as flags
		"https://youtu.be/" + videoID,
	}...)

	if err := s.runWithStallDetector(ctx, outPath, "yt-dlp", args...); err != nil {
		return fmt.Errorf("download full: yt-dlp: %w", err)
	}

	if err := s.storage.Commit(ctx, s.storage.Key(videoID, "original.wav"), outPath); err != nil {
		return fmt.Errorf("download full: commit: %w", err)
	}

	return nil
}
