package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"cantus/backend/services"
)

func TestPythonYouTubeService_Search(t *testing.T) {
	const (
		videoID1 = "dQw4w9WgXcQ"
		videoID2 = "aaaaaaaaaaa"
	)

	signer := newTestSigner(t)
	sig1 := signer.Sign(videoID1)
	sig2 := signer.Sign(videoID2)

	twoItemBody := `{
		"items": [
			{"videoId":"dQw4w9WgXcQ","title":"Never Gonna Give You Up","artist":"Rick Astley","album":"Whenever You Need Somebody","duration_sec":213,"thumbnail_url":"https://example.com/1.jpg"},
			{"videoId":"aaaaaaaaaaa","title":"Song Two","artist":"Artist Two","album":null,"duration_sec":180,"thumbnail_url":"https://example.com/2.jpg"}
		],
		"has_more": true
	}`

	tests := []struct {
		name        string
		query       string
		limit       int
		offset      int
		transport   roundTripperFunc
		wantErr     bool
		errContains string
		wantLen     int
		wantHasMore bool
		wantSigs    []string
	}{
		{
			name:   "happy path two items has_more true",
			query:  "rick astley",
			limit:  10,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					t.Errorf("method: got %q, want POST", r.Method)
				}
				if r.URL.Path != "/search" {
					t.Errorf("path: got %q, want /search", r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("Content-Type: got %q, want application/json", ct)
				}
				var body struct {
					Query  string `json:"query"`
					Limit  int    `json:"limit"`
					Offset int    `json:"offset"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if body.Query != "rick astley" {
					t.Errorf("body.query: got %q, want %q", body.Query, "rick astley")
				}
				if body.Limit != 10 {
					t.Errorf("body.limit: got %d, want 10", body.Limit)
				}
				if body.Offset != 0 {
					t.Errorf("body.offset: got %d, want 0", body.Offset)
				}
				return makeResponse(http.StatusOK, twoItemBody), nil
			}),
			wantErr:     false,
			wantLen:     2,
			wantHasMore: true,
			wantSigs:    []string{sig1, sig2},
		},
		{
			name:   "invalid videoID in one item is dropped",
			query:  "test",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				body := `{
					"items": [
						{"videoId":"dQw4w9WgXcQ","title":"Good","artist":"A","album":null,"duration_sec":100,"thumbnail_url":"https://example.com/t.jpg"},
						{"videoId":"bad/slash!!","title":"Bad","artist":"B","album":null,"duration_sec":100,"thumbnail_url":"https://example.com/t.jpg"}
					],
					"has_more": false
				}`
				return makeResponse(http.StatusOK, body), nil
			}),
			wantErr:     false,
			wantLen:     1,
			wantHasMore: false,
			wantSigs:    []string{sig1},
		},
		{
			name:   "empty items returns empty slice not nil",
			query:  "nothing",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, `{"items":[],"has_more":false}`), nil
			}),
			wantErr:     false,
			wantLen:     0,
			wantHasMore: false,
		},
		{
			name:   "upstream 500 returns error with status code",
			query:  "test",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
			}),
			wantErr:     true,
			errContains: "500",
		},
		{
			name:   "upstream 400 returns error with status code",
			query:  "test",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusBadRequest, `{"error":"bad"}`), nil
			}),
			wantErr:     true,
			errContains: "400",
		},
		{
			name:   "malformed JSON body returns error",
			query:  "test",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, "not json"), nil
			}),
			wantErr: true,
		},
		{
			name:   "network error is returned",
			query:  "test",
			limit:  5,
			offset: 0,
			transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return nil, errors.New("net down")
			}),
			wantErr:     true,
			errContains: "net down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.transport}
			svc := services.NewPythonYouTubeService("http://localhost:8090", client, signer, nil, nil)

			page, err := svc.Search(context.Background(), tt.query, tt.limit, tt.offset)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Search: got nil error, want error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("Search: unexpected error: %v", err)
			}
			if len(page.Items) != tt.wantLen {
				t.Errorf("len(Items): got %d, want %d", len(page.Items), tt.wantLen)
			}
			if page.HasMore != tt.wantHasMore {
				t.Errorf("HasMore: got %v, want %v", page.HasMore, tt.wantHasMore)
			}
			for i, wantSig := range tt.wantSigs {
				if i >= len(page.Items) {
					break
				}
				if page.Items[i].Sig != wantSig {
					t.Errorf("Items[%d].Sig: got %q, want %q", i, page.Items[i].Sig, wantSig)
				}
			}
			if tt.wantLen == 0 && page.Items == nil {
				t.Errorf("Items: got nil, want empty slice")
			}
		})
	}
}

func TestPythonYouTubeService_Search_ContextCanceled(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "canceled context returns context.Canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signer := newTestSigner(t)
			transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return makeResponse(http.StatusOK, `{"items":[],"has_more":false}`), nil
			})
			client := &http.Client{Transport: transport}
			svc := services.NewPythonYouTubeService("http://localhost:8090", client, signer, nil, nil)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := svc.Search(ctx, "test", 5, 0)
			if err == nil {
				t.Fatalf("Search with canceled ctx: got nil error, want error")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error %v: expected errors.Is(err, context.Canceled) to be true", err)
			}
		})
	}
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
	committed []struct{ videoID, name, localPath string }
	commitErr error
}

func (f *fakeStorage) LocalPath(_ context.Context, _, _ string) (string, error) { return "", nil }
func (f *fakeStorage) Has(_ context.Context, _, _ string) (bool, error)         { return false, nil }
func (f *fakeStorage) Open(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeStorage) Commit(_ context.Context, videoID, name, localPath string) error {
	f.committed = append(f.committed, struct{ videoID, name, localPath string }{videoID, name, localPath})
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
				"http://localhost:8090",
				&http.Client{},
				signer,
				store,
				runner,
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
				mustContainArg(t, args, "*30-60")
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
				if c.videoID != tt.videoID {
					t.Errorf("Commit videoID: got %q, want %q", c.videoID, tt.videoID)
				}
				if c.name != "preview.mp3" {
					t.Errorf("Commit name: got %q, want %q", c.name, "preview.mp3")
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
				"http://localhost:8090",
				&http.Client{},
				signer,
				store,
				runner,
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
				"http://localhost:8090",
				&http.Client{},
				signer,
				store,
				runner,
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
				if c.videoID != tt.videoID {
					t.Errorf("Commit videoID: got %q, want %q", c.videoID, tt.videoID)
				}
				if c.name != "original.wav" {
					t.Errorf("Commit name: got %q, want %q", c.name, "original.wav")
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
				"http://localhost:8090",
				&http.Client{},
				signer,
				store,
				runner,
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
