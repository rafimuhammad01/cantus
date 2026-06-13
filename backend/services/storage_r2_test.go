package services_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cantus/backend/services"
)

// newR2WithMock starts an httptest server using handler and returns an R2Storage
// wired to it. Callers must defer srv.Close().
func newR2WithMock(t *testing.T, handler http.HandlerFunc) (*services.R2Storage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s, err := services.NewR2Storage(services.R2Config{
		AccountID:       "acct",
		AccessKeyID:     "k",
		SecretAccessKey: "s",
		Bucket:          "cantus-cache",
		PresignTTL:      time.Minute,
		Endpoint:        srv.URL,
	})
	if err != nil {
		srv.Close()
		t.Fatalf("NewR2Storage: %v", err)
	}
	return s, srv
}

// TestR2Storage_Key_isPathJoin verifies Key produces forward-slash-joined
// "videoID/name" strings without any I/O.
func TestR2Storage_Key_isPathJoin(t *testing.T) {
	// Key is a pure function — use a dummy handler; no requests are made.
	s, srv := newR2WithMock(t, func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()

	cases := []struct {
		name    string
		videoID string
		objName string
		want    string
	}{
		{"flat", "abc12345678", "melody.json", "abc12345678/melody.json"},
		{"nested", "abc12345678", "shifted/0/audio.mp3", "abc12345678/shifted/0/audio.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Key(tc.videoID, tc.objName); got != tc.want {
				t.Errorf("Key(%q, %q) = %q, want %q", tc.videoID, tc.objName, got, tc.want)
			}
		})
	}
}

// TestR2Storage_Has verifies HEAD → Has logic: 200+non-zero = true, 200+zero = false, 404 = false.
func TestR2Storage_Has(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantHas bool
		wantErr bool
	}{
		{
			name: "200 with non-zero content-length",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("method = %s, want HEAD", r.Method)
				}
				// Write a body so Go's http server sets Content-Length automatically.
				w.Header().Set("Content-Length", "3")
				w.WriteHeader(http.StatusOK)
			},
			wantHas: true,
		},
		{
			name: "200 with zero content-length",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusOK)
			},
			wantHas: false,
		},
		{
			name: "404 not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantHas: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newR2WithMock(t, tc.handler)
			defer srv.Close()

			has, err := s.Has(context.Background(), "abc/melody.json")
			if tc.wantErr {
				if err == nil {
					t.Error("Has: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Has: unexpected error: %v", err)
			}
			if has != tc.wantHas {
				t.Errorf("Has = %v, want %v", has, tc.wantHas)
			}
		})
	}
}

// TestR2Storage_SignGetSignPut verifies that presigned URLs contain the key and
// the AWS v4 signature query parameter.
func TestR2Storage_SignGetSignPut(t *testing.T) {
	s, srv := newR2WithMock(t, func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()

	cases := []struct {
		name string
		sign func(context.Context, string) (string, error)
	}{
		{"SignGet", s.SignGet},
		{"SignPut", s.SignPut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := tc.sign(context.Background(), "abc/melody.json")
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !strings.Contains(u, "abc/melody.json") {
				t.Errorf("%s URL missing key: %q", tc.name, u)
			}
			if !strings.Contains(u, "X-Amz-Signature") {
				t.Errorf("%s URL missing X-Amz-Signature: %q", tc.name, u)
			}
		})
	}
}

// TestR2Storage_Open_returnsBody verifies that Open streams the object body correctly.
func TestR2Storage_Open_returnsBody(t *testing.T) {
	const wantBody = "audio-bytes"

	s, srv := newR2WithMock(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Open: method = %s, want GET", r.Method)
		}
		_, _ = io.WriteString(w, wantBody)
	})
	defer srv.Close()

	rc, err := s.Open(context.Background(), "abc/x.mp3")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != wantBody {
		t.Errorf("Open body = %q, want %q", string(body), wantBody)
	}
}

// TestR2Storage_Commit_uploadsFile verifies that Commit sends a PUT request with
// the file body to the correct key path.
func TestR2Storage_Commit_uploadsFile(t *testing.T) {
	const wantContent = "raw-audio-data"

	var gotMethod, gotPath string
	var gotBody []byte

	s, srv := newR2WithMock(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	// Write a temp file to commit.
	tmp := t.TempDir()
	localPath := tmp + "/audio.mp3"
	if err := writeFileForR2Test(t, localPath, wantContent); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := s.Commit(context.Background(), "abc/audio.mp3", localPath); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("Commit method = %s, want PUT", gotMethod)
	}
	if !strings.Contains(gotPath, "abc/audio.mp3") {
		t.Errorf("Commit path = %q, want path containing abc/audio.mp3", gotPath)
	}
	if string(gotBody) != wantContent {
		t.Errorf("Commit body = %q, want %q", string(gotBody), wantContent)
	}
}

// writeFileForR2Test creates a file at path with the given content.
// Extracted so it can be called from both Commit test and any future tests.
func TestR2Storage_Verify(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		wantErrIs error
	}{
		{
			name: "non-empty HEAD 200 → nil",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "42")
				w.WriteHeader(http.StatusOK)
			},
			wantErrIs: nil,
		},
		{
			name: "zero-size HEAD 200 → ErrObjectNotMaterialized",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusOK)
			},
			wantErrIs: services.ErrObjectNotMaterialized,
		},
		{
			name: "HEAD 404 → ErrObjectNotMaterialized",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErrIs: services.ErrObjectNotMaterialized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			s, err := services.NewR2Storage(services.R2Config{
				AccountID: "acct", AccessKeyID: "k", SecretAccessKey: "s",
				Bucket: "cantus-cache", PresignTTL: time.Minute, Endpoint: srv.URL,
			})
			if err != nil {
				t.Fatalf("NewR2Storage: %v", err)
			}
			err = s.Verify(context.Background(), "abc/melody.json")
			if tc.wantErrIs == nil {
				if err != nil {
					t.Fatalf("Verify: got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("Verify: got %v, want errors.Is(%v)", err, tc.wantErrIs)
			}
		})
	}
}

func writeFileForR2Test(t *testing.T, filePath, content string) error {
	t.Helper()
	return writeFileBytes(filePath, []byte(content))
}

func writeFileBytes(filePath string, data []byte) error {
	return os.WriteFile(filePath, data, 0o644)
}
