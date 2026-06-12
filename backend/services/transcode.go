package services

import (
	"context"
	"fmt"
	"os/exec"
)

// TranscodeFunc transcodes a WAV file to 128kbps MP3 at the given output path.
// Tests inject a fake; production uses FFmpegTranscode.
type TranscodeFunc func(ctx context.Context, inputPath, outputPath string) error

// FFmpegTranscode is the production TranscodeFunc implementation.
// It shells out to ffmpeg, encoding at 128kbps MP3 with minimal log output.
func FFmpegTranscode(ctx context.Context, inputPath, outputPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", inputPath,
		"-codec:a", "libmp3lame", "-b:a", "128k",
		"--", outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg transcode: %w: %s", err, out)
	}
	return nil
}
