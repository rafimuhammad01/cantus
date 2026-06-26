package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cantus/backend/services"
)

// recordingRunner records every command invocation for assertions.
type recordingRunner struct {
	calls [][]string
	// onCall optionally writes the expected output file so downstream steps can proceed,
	// or returns a non-nil error to simulate command failure.
	onCall func(name string, args []string) error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.onCall != nil {
		return r.onCall(name, args)
	}
	return nil
}

func TestCLIShifter_Shift(t *testing.T) {
	tests := []struct {
		name      string
		semitones float64
		wantCalls []string // first token of each invocation, in order
		wantPFlag string   // expected -p semitone string (empty = rubberband not called)
	}{
		{
			name:      "mp3→mp3 non-zero shift uses only rubberband",
			semitones: -3,
			wantCalls: []string{"rubberband"},
			wantPFlag: "-3",
		},
		{
			name:      "mp3→mp3 zero-shift copies file, no rubberband",
			semitones: 0,
			wantCalls: []string{},
			wantPFlag: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in"+services.AudioExt)
			out := filepath.Join(dir, "out"+services.AudioExt)
			if err := os.WriteFile(in, []byte("audio bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{
				onCall: func(_ string, args []string) error {
					// Touch the output file argument so downstream steps see content.
					last := args[len(args)-1]
					return os.WriteFile(last, []byte("x"), 0o644)
				},
			}
			s := services.NewCLIShifter("rubberband", runner)
			if err := s.Shift(context.Background(), in, out, tt.semitones); err != nil {
				t.Fatalf("Shift: %v", err)
			}
			if len(runner.calls) != len(tt.wantCalls) {
				t.Fatalf("calls: got %d, want %d (%v)", len(runner.calls), len(tt.wantCalls), runner.calls)
			}
			for i, want := range tt.wantCalls {
				if runner.calls[i][0] != want {
					t.Errorf("call[%d]: got %q, want %q", i, runner.calls[i][0], want)
				}
			}
			if tt.wantPFlag != "" {
				for _, call := range runner.calls {
					if call[0] != "rubberband" {
						continue
					}
					joined := strings.Join(call, " ")
					if !strings.Contains(joined, "-p "+tt.wantPFlag) {
						t.Errorf("rubberband args missing -p %s: %q", tt.wantPFlag, joined)
					}
				}
			}
			if _, err := os.Stat(out); err != nil {
				t.Errorf("output not created: %v", err)
			}
		})
	}
}

func TestCLIShifter_Shift_RunnerError(t *testing.T) {
	tests := []struct {
		name      string
		semitones float64
	}{
		{name: "rubberband failure surfaces (non-zero semitones)", semitones: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in"+services.AudioExt)
			out := filepath.Join(dir, "out"+services.AudioExt)
			_ = os.WriteFile(in, []byte("x"), 0o644)
			runner := &recordingRunner{onCall: func(name string, _ []string) error {
				if name == "rubberband" {
					return errors.New("boom")
				}
				return nil
			}}
			s := services.NewCLIShifter("rubberband", runner)
			if err := s.Shift(context.Background(), in, out, tt.semitones); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}
