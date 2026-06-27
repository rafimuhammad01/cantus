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
// for MP3 ↔ WAV transcoding around it.
//
// Defensive split: libsndfile MP3 read landed in 1.1.0 and MP3 write in 1.2.0.
// Older runtime images (Debian 11, Ubuntu 22.04 and below) ship 1.0.x and
// silently stall on MP3 I/O. By bracketing rubberband with ffmpeg decode/encode
// we make the pipeline portable to any modern runtime.
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

// Shift decodes inputPath (MP3) to WAV, runs rubberband, then encodes the
// result back to MP3 at outputPath. When semitones is 0, the input is copied
// straight through without touching rubberband or ffmpeg.
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

	wavIn, err := os.CreateTemp(outDir, "shift-in-*.wav")
	if err != nil {
		return fmt.Errorf("shift: tempfile in: %w", err)
	}
	wavInPath := wavIn.Name()
	_ = wavIn.Close()
	defer func() { _ = os.Remove(wavInPath) }()

	if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", inputPath, "-ar", "44100", "-ac", "2", wavInPath); err != nil {
		return fmt.Errorf("shift: ffmpeg decode: %w", err)
	}

	wavOut, err := os.CreateTemp(outDir, "shift-out-*.wav")
	if err != nil {
		return fmt.Errorf("shift: tempfile out: %w", err)
	}
	wavOutPath := wavOut.Name()
	_ = wavOut.Close()
	defer func() { _ = os.Remove(wavOutPath) }()

	pArg := strconv.FormatFloat(semitones, 'f', -1, 64)
	if err := s.Runner.Run(ctx, s.Rubberband, "-p", pArg, wavInPath, wavOutPath); err != nil {
		return fmt.Errorf("shift: rubberband: %w", err)
	}
	if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", wavOutPath, "-b:a", "128k", "-ar", "44100", outputPath); err != nil {
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
