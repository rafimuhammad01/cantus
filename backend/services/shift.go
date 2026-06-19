package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Shifter pitch-shifts an audio file by a number of semitones.
// inputPath and outputPath are local filesystem paths; the implementation
// handles MP3↔WAV transcoding internally so callers don't have to care.
type Shifter interface {
	Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error
}

// CLIShifter shells out to the `rubberband` CLI for pitch shifting and to
// `ffmpeg` for MP3↔WAV transcoding when the input or output is .mp3.
type CLIShifter struct {
	Rubberband string
	FFmpeg     string
	Runner     CommandRunner
}

// NewCLIShifter returns a CLIShifter with the given binary paths and runner.
// Pass "rubberband"/"ffmpeg" to resolve via $PATH.
func NewCLIShifter(rubberband, ffmpeg string, runner CommandRunner) *CLIShifter {
	return &CLIShifter{Rubberband: rubberband, FFmpeg: ffmpeg, Runner: runner}
}

// Shift decodes (if MP3) → rubberband-shifts → encodes (if MP3 out).
// When semitones is exactly 0, rubberband is skipped entirely: the WAV input
// is passed straight to ffmpeg for encoding (or copied for WAV output), saving
// the full rubberband processing pass on large files.
// Scratch tempfiles live in the same directory as outputPath and are cleaned up.
func (s *CLIShifter) Shift(ctx context.Context, inputPath, outputPath string, semitones float64) error {
	if _, err := os.Stat(inputPath); err != nil {
		return fmt.Errorf("shift: stat input: %w", err)
	}
	outDir := filepath.Dir(outputPath)
	if outDir == "" {
		outDir = "."
	}

	inIsMP3 := strings.EqualFold(filepath.Ext(inputPath), ".mp3")
	outIsMP3 := strings.EqualFold(filepath.Ext(outputPath), ".mp3")

	var scratch []string
	defer func() {
		for _, p := range scratch {
			_ = os.Remove(p)
		}
	}()

	wavIn := inputPath
	if inIsMP3 {
		f, err := os.CreateTemp(outDir, "shift-in-*.wav")
		if err != nil {
			return fmt.Errorf("shift: tempfile in: %w", err)
		}
		_ = f.Close()
		wavIn = f.Name()
		scratch = append(scratch, wavIn)
		if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", inputPath, "-ar", "44100", "-ac", "2", wavIn); err != nil {
			return fmt.Errorf("shift: ffmpeg decode: %w", err)
		}
	}

	// When semitones == 0, skip rubberband entirely — the output is audibly identical
	// to the input, so we only need to transcode format if required.
	if semitones == 0 {
		if outIsMP3 {
			if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", wavIn, "-b:a", "128k", "-ar", "44100", outputPath); err != nil {
				return fmt.Errorf("shift: ffmpeg encode (zero-shift): %w", err)
			}
		} else {
			// WAV → WAV with 0 semitones: copy the file.
			if err := copyFile(wavIn, outputPath); err != nil {
				return fmt.Errorf("shift: copy (zero-shift): %w", err)
			}
		}
		return nil
	}

	wavOut := outputPath
	if outIsMP3 {
		f, err := os.CreateTemp(outDir, "shift-out-*.wav")
		if err != nil {
			return fmt.Errorf("shift: tempfile out: %w", err)
		}
		_ = f.Close()
		wavOut = f.Name()
		scratch = append(scratch, wavOut)
	}

	pArg := strconv.FormatFloat(semitones, 'f', -1, 64)
	if err := s.Runner.Run(ctx, s.Rubberband, "-p", pArg, wavIn, wavOut); err != nil {
		return fmt.Errorf("shift: rubberband: %w", err)
	}

	if outIsMP3 {
		if err := s.Runner.Run(ctx, s.FFmpeg, "-y", "-i", wavOut, "-b:a", "128k", "-ar", "44100", outputPath); err != nil {
			return fmt.Errorf("shift: ffmpeg encode: %w", err)
		}
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
