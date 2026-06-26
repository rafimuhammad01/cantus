package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
)

// Shifter pitch-shifts an audio file by a number of semitones.
// inputPath and outputPath are local filesystem paths; rubberband handles
// MP3 input and output natively so no format conversion is needed.
type Shifter interface {
	Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
}

// CLIShifter shells out to the `rubberband` CLI for pitch shifting.
// rubberband 4.0.0+ reads and writes MP3 natively via libsndfile, so no
// ffmpeg transcode step is required.
type CLIShifter struct {
	Rubberband string
	Runner     CommandRunner
}

// NewCLIShifter returns a CLIShifter with the given binary path and runner.
// Pass "rubberband" to resolve via $PATH.
func NewCLIShifter(rubberband string, runner CommandRunner) *CLIShifter {
	return &CLIShifter{Rubberband: rubberband, Runner: runner}
}

// Shift pitch-shifts inputPath by semitones and writes the result to outputPath.
// When semitones is exactly 0, rubberband is skipped and the file is copied directly.
func (s *CLIShifter) Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error {
	if _, err := os.Stat(inputPath); err != nil {
		return fmt.Errorf("shift: stat input: %w", err)
	}
	if semitones == 0 {
		return copyFile(inputPath, outputPath)
	}
	pArg := strconv.FormatFloat(semitones, 'f', -1, 64)
	if err := s.Runner.Run(ctx, s.Rubberband, "-p", pArg, inputPath, outputPath); err != nil {
		return fmt.Errorf("shift: rubberband: %w", err)
	}
	return nil
}

// copyFile copies src to dst, creating dst if it does not exist.
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
