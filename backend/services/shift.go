package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Shifter pitch-shifts an audio file by a number of semitones.
type Shifter interface {
	Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
}

// CLIShifter shells out to `rubberband` for pitch shifting, and to `ffmpeg`
// to encode the rubberband WAV output back to MP3.
//
// Defensive split: libsndfile gained MP3 read in 1.1.0 (universal across modern
// distros) but MP3 write only in 1.2.0 (missing on Debian 11 / Ubuntu 22.04).
// Letting rubberband write WAV → ffmpeg encoding to MP3 makes the pipeline
// portable to older runtime images.
type CLIShifter struct {
	Rubberband string
	FFmpeg     string
	Runner     CommandRunner
}

// NewCLIShifter returns a CLIShifter wired to the given binary paths.
// Pass "rubberband" / "ffmpeg" to resolve via $PATH.
func NewCLIShifter(rubberband, ffmpeg string, runner CommandRunner) *CLIShifter {
	return &CLIShifter{Rubberband: rubberband, FFmpeg: ffmpeg, Runner: runner}
}

// Shift pitch-shifts inputPath by semitones and writes the result to outputPath.
// When semitones is 0, rubberband is skipped and the file is copied directly.
// rubberband always writes to a WAV tempfile which is then ffmpeg-encoded to
// MP3 — this avoids depending on libsndfile MP3 write (1.2.0+ only).
func (s *CLIShifter) Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error {
	if _, err := os.Stat(inputPath); err != nil {
		return fmt.Errorf("shift: stat input: %w", err)
	}
	if semitones == 0 {
		return copyFile(inputPath, outputPath)
	}

	outDir := filepath.Dir(outputPath)
	if outDir == "" {
		outDir = "."
	}
	tmpWav, err := os.CreateTemp(outDir, "shift-out-*.wav")
	if err != nil {
		return fmt.Errorf("shift: tempfile out: %w", err)
	}
	tmpWavPath := tmpWav.Name()
	_ = tmpWav.Close()
	defer func() { _ = os.Remove(tmpWavPath) }()

	pArg := strconv.FormatFloat(semitones, 'f', -1, 64)
	if err := s.Runner.Run(ctx, s.Rubberband, "-p", pArg, inputPath, tmpWavPath); err != nil {
		return fmt.Errorf("shift: rubberband: %w", err)
	}
	if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", tmpWavPath, "-b:a", "128k", "-ar", "44100", outputPath); err != nil {
		return fmt.Errorf("shift: ffmpeg encode: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
