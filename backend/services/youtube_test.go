package services_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"cantus/backend/models"
	"cantus/backend/services"
)

func TestPythonYouTubeService_SearchDelegates(t *testing.T) {
	tests := []struct {
		name    string
		page    services.SearchPage
		wantIDs []string
	}{
		{
			name: "returns whatever YTMusicSearch returns",
			page: services.SearchPage{
				Items: []models.SearchResult{
					{VideoID: "aaaaaaaaaaa", Sig: "sig"},
				},
				HasMore: false,
			},
			wantIDs: []string{"aaaaaaaaaaa"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSearchDelegate{page: tt.page}
			svc := services.NewPythonYouTubeService(fake, nil, nil, services.ExecRunner{}, "")
			got, err := svc.Search(context.Background(), "q", 5, 0)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got.Items) != len(tt.wantIDs) {
				t.Fatalf("len: got %d, want %d", len(got.Items), len(tt.wantIDs))
			}
			for i, want := range tt.wantIDs {
				if got.Items[i].VideoID != want {
					t.Errorf("Items[%d].VideoID: got %q, want %q", i, got.Items[i].VideoID, want)
				}
			}
		})
	}
}

type fakeSearchDelegate struct{ page services.SearchPage }

func (f *fakeSearchDelegate) Search(_ context.Context, _ string, _, _ int) (services.SearchPage, error) {
	return f.page, nil
}

// fakeRunner is a test double for CommandRunner that records invocations and
// optionally writes a fake output file to simulate yt-dlp's output.
type fakeRunner struct {
	err        error
	writeBytes []byte // if non-nil, write to the path following -o
	gotName    string
	gotArgs    []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.gotName = name
	f.gotArgs = args
	if f.err != nil {
		return f.err
	}
	// Simulate yt-dlp writing the output file: find -o arg and write bytes there.
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && f.writeBytes != nil {
			if err := os.WriteFile(args[i+1], f.writeBytes, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// fakeStorage is a test double for Storage that records Commit calls.
type fakeStorage struct {
	committed []struct{ key, localPath string }
	commitErr error
}

func (f *fakeStorage) Key(videoID, name string) string                     { return videoID + "/" + name }
func (f *fakeStorage) Has(_ context.Context, _ string) (bool, error)       { return false, nil }
func (f *fakeStorage) SignGet(_ context.Context, _ string) (string, error) { return "", nil }
func (f *fakeStorage) SignPut(_ context.Context, _ string) (string, error) { return "", nil }
func (f *fakeStorage) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeStorage) Verify(_ context.Context, _ string) error { return nil }
func (f *fakeStorage) Commit(_ context.Context, key, localPath string) error {
	f.committed = append(f.committed, struct{ key, localPath string }{key, localPath})
	return f.commitErr
}

func TestPythonYouTubeService_DownloadPreview(t *testing.T) {
	const goodVideoID = "dQw4w9WgXcQ"

	tests := []struct {
		name string

		videoID    string
		runnerErr  error
		writeBytes []byte // bytes fakeRunner writes to simulate yt-dlp output
		commitErr  error

		wantErr          bool
		errContains      string
		wantRunnerCalled bool
		wantCommitCount  int
	}{
		{
			name:             "happy path",
			videoID:          goodVideoID,
			writeBytes:       []byte("fake mp3 data"),
			wantErr:          false,
			wantRunnerCalled: true,
			wantCommitCount:  1,
		},
		{
			name:             "invalid videoID with slash",
			videoID:          "bad/slash!!",
			wantErr:          true,
			errContains:      "invalid videoID",
			wantRunnerCalled: false,
			wantCommitCount:  0,
		},
		{
			name:             "invalid videoID empty",
			videoID:          "",
			wantErr:          true,
			errContains:      "invalid videoID",
			wantRunnerCalled: false,
			wantCommitCount:  0,
		},
		{
			name:             "runner returns error",
			videoID:          goodVideoID,
			runnerErr:        errors.New("yt-dlp failed"),
			wantErr:          true,
			errContains:      "yt-dlp",
			wantRunnerCalled: true,
			wantCommitCount:  0,
		},
		{
			name:             "storage commit returns error",
			videoID:          goodVideoID,
			writeBytes:       []byte("fake mp3 data"),
			commitErr:        errors.New("disk full"),
			wantErr:          true,
			errContains:      "disk full",
			wantRunnerCalled: true,
			wantCommitCount:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{
				err:        tt.runnerErr,
				writeBytes: tt.writeBytes,
			}
			store := &fakeStorage{commitErr: tt.commitErr}

			signer := newTestSigner(t)
			svc := services.NewPythonYouTubeService(
				nil,
				signer,
				store,
				runner,
				"",
			)

			err := svc.DownloadPreview(context.Background(), tt.videoID)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("DownloadPreview: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("DownloadPreview: unexpected error: %v", err)
				}
			}

			// Check runner invocation.
			if tt.wantRunnerCalled && runner.gotName == "" {
				t.Error("runner: expected to be called, was not")
			}
			if !tt.wantRunnerCalled && runner.gotName != "" {
				t.Errorf("runner: expected NOT to be called, but got name=%q", runner.gotName)
			}

			// When runner was called, validate the yt-dlp arguments.
			if runner.gotName != "" {
				if runner.gotName != "yt-dlp" {
					t.Errorf("runner name: got %q, want %q", runner.gotName, "yt-dlp")
				}

				args := runner.gotArgs
				mustContainArg(t, args, "--download-sections")
				mustContainArg(t, args, "*0-30")
				mustContainArg(t, args, "-x")
				mustContainArg(t, args, "--audio-format")
				mustContainArg(t, args, "mp3")

				// -o must be followed by a path ending in preview.mp3.
				oIdx := indexArg(args, "-o")
				if oIdx < 0 || oIdx+1 >= len(args) {
					t.Error("args: missing -o <path>")
				} else if !strings.HasSuffix(args[oIdx+1], "preview.mp3") {
					t.Errorf("args: -o path %q does not end in preview.mp3", args[oIdx+1])
				}

				// -- separator must appear before the URL.
				dashDashIdx := indexArg(args, "--")
				if dashDashIdx < 0 {
					t.Error("args: missing -- separator")
				}

				wantURL := "https://youtu.be/" + tt.videoID
				urlIdx := indexArg(args, wantURL)
				if urlIdx < 0 {
					t.Errorf("args: URL %q not found", wantURL)
				} else if dashDashIdx >= urlIdx {
					t.Errorf("args: -- (idx %d) must appear before URL (idx %d)", dashDashIdx, urlIdx)
				}

				// When commit is expected, the -o path and localPath passed to Commit must match.
				if tt.wantCommitCount > 0 && len(store.committed) > 0 {
					oPath := args[oIdx+1]
					if store.committed[0].localPath != oPath {
						t.Errorf("Commit localPath: got %q, want %q (same as -o)", store.committed[0].localPath, oPath)
					}
				}
			}

			// Check Storage.Commit call count.
			if len(store.committed) != tt.wantCommitCount {
				t.Errorf("Commit calls: got %d, want %d", len(store.committed), tt.wantCommitCount)
			}
			if tt.wantCommitCount > 0 && len(store.committed) > 0 {
				c := store.committed[0]
				wantKey := tt.videoID + "/preview.mp3"
				if c.key != wantKey {
					t.Errorf("Commit key: got %q, want %q", c.key, wantKey)
				}
			}
		})
	}
}

// TestPythonYouTubeService_DownloadPreview_ContextCanceled checks that a
// canceled context propagates an error without committing to storage.
func TestPythonYouTubeService_DownloadPreview_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns error without commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{writeBytes: []byte("fake")}
			store := &fakeStorage{}
			signer := newTestSigner(t)
			svc := services.NewPythonYouTubeService(
				nil,
				signer,
				store,
				runner,
				"",
			)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := svc.DownloadPreview(ctx, "dQw4w9WgXcQ")
			if err == nil {
				t.Fatal("DownloadPreview with canceled ctx: got nil error, want error")
			}
			if len(store.committed) > 0 {
				t.Errorf("Commit: expected 0 calls, got %d", len(store.committed))
			}
		})
	}
}

func TestPythonYouTubeService_DownloadFull(t *testing.T) {
	const goodVideoID = "dQw4w9WgXcQ"

	tests := []struct {
		name string

		videoID    string
		runnerErr  error
		writeBytes []byte // bytes fakeRunner writes to simulate yt-dlp output
		commitErr  error

		wantErr          bool
		errContains      string
		wantRunnerCalled bool
		wantCommitCount  int
	}{
		{
			name:             "happy path",
			videoID:          goodVideoID,
			writeBytes:       []byte("fake wav data"),
			wantErr:          false,
			wantRunnerCalled: true,
			wantCommitCount:  1,
		},
		{
			name:             "invalid videoID with slash",
			videoID:          "bad/slash!!",
			wantErr:          true,
			errContains:      "invalid videoID",
			wantRunnerCalled: false,
			wantCommitCount:  0,
		},
		{
			name:             "invalid videoID empty",
			videoID:          "",
			wantErr:          true,
			errContains:      "invalid videoID",
			wantRunnerCalled: false,
			wantCommitCount:  0,
		},
		{
			name:             "runner returns error",
			videoID:          goodVideoID,
			runnerErr:        errors.New("yt-dlp failed"),
			wantErr:          true,
			errContains:      "yt-dlp",
			wantRunnerCalled: true,
			wantCommitCount:  0,
		},
		{
			name:             "storage commit returns error",
			videoID:          goodVideoID,
			writeBytes:       []byte("fake wav data"),
			commitErr:        errors.New("disk full"),
			wantErr:          true,
			errContains:      "disk full",
			wantRunnerCalled: true,
			wantCommitCount:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{
				err:        tt.runnerErr,
				writeBytes: tt.writeBytes,
			}
			store := &fakeStorage{commitErr: tt.commitErr}

			signer := newTestSigner(t)
			svc := services.NewPythonYouTubeService(
				nil,
				signer,
				store,
				runner,
				"",
			)

			err := svc.DownloadFull(context.Background(), tt.videoID)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("DownloadFull: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("DownloadFull: unexpected error: %v", err)
				}
			}

			if tt.wantRunnerCalled && runner.gotName == "" {
				t.Error("runner: expected to be called, was not")
			}
			if !tt.wantRunnerCalled && runner.gotName != "" {
				t.Errorf("runner: expected NOT to be called, but got name=%q", runner.gotName)
			}

			if runner.gotName != "" {
				if runner.gotName != "yt-dlp" {
					t.Errorf("runner name: got %q, want %q", runner.gotName, "yt-dlp")
				}

				args := runner.gotArgs

				// MUST be a full-song download — no --download-sections flag.
				if indexArg(args, "--download-sections") >= 0 {
					t.Error("args: full-song download must NOT use --download-sections")
				}

				mustContainArg(t, args, "-x")
				mustContainArg(t, args, "--audio-format")
				mustContainArg(t, args, "wav")

				oIdx := indexArg(args, "-o")
				if oIdx < 0 || oIdx+1 >= len(args) {
					t.Error("args: missing -o <path>")
				} else if !strings.HasSuffix(args[oIdx+1], "original.wav") {
					t.Errorf("args: -o path %q does not end in original.wav", args[oIdx+1])
				}

				dashDashIdx := indexArg(args, "--")
				if dashDashIdx < 0 {
					t.Error("args: missing -- separator")
				}

				wantURL := "https://youtu.be/" + tt.videoID
				urlIdx := indexArg(args, wantURL)
				if urlIdx < 0 {
					t.Errorf("args: URL %q not found", wantURL)
				} else if dashDashIdx >= urlIdx {
					t.Errorf("args: -- (idx %d) must appear before URL (idx %d)", dashDashIdx, urlIdx)
				}

				if tt.wantCommitCount > 0 && len(store.committed) > 0 {
					oPath := args[oIdx+1]
					if store.committed[0].localPath != oPath {
						t.Errorf("Commit localPath: got %q, want %q (same as -o)", store.committed[0].localPath, oPath)
					}
				}
			}

			if len(store.committed) != tt.wantCommitCount {
				t.Errorf("Commit calls: got %d, want %d", len(store.committed), tt.wantCommitCount)
			}
			if tt.wantCommitCount > 0 && len(store.committed) > 0 {
				c := store.committed[0]
				wantKey := tt.videoID + "/original.wav"
				if c.key != wantKey {
					t.Errorf("Commit key: got %q, want %q", c.key, wantKey)
				}
			}
		})
	}
}

// TestPythonYouTubeService_DownloadFull_ContextCanceled checks that a
// canceled context propagates an error without committing to storage.
func TestPythonYouTubeService_DownloadFull_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns error without commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{writeBytes: []byte("fake")}
			store := &fakeStorage{}
			signer := newTestSigner(t)
			svc := services.NewPythonYouTubeService(
				nil,
				signer,
				store,
				runner,
				"",
			)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := svc.DownloadFull(ctx, "dQw4w9WgXcQ")
			if err == nil {
				t.Fatal("DownloadFull with canceled ctx: got nil error, want error")
			}
			if len(store.committed) > 0 {
				t.Errorf("Commit: expected 0 calls, got %d", len(store.committed))
			}
		})
	}
}

// mustContainArg fails if arg is not present in args.
func mustContainArg(t *testing.T, args []string, arg string) {
	t.Helper()
	if indexArg(args, arg) < 0 {
		t.Errorf("args: expected %q to be present in %v", arg, args)
	}
}

// indexArg returns the first index of arg in args, or -1 if not found.
func indexArg(args []string, arg string) int {
	for i, a := range args {
		if a == arg {
			return i
		}
	}
	return -1
}
