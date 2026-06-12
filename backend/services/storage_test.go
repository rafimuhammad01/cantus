package services_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cantus/backend/services"
)

// writeFileAged writes content to path (creating parent dirs), then backdates
// the mtime by age. Used to verify that old files are still reported as present
// (permanent cache — no TTL eviction).
func writeFileAged(t *testing.T, path, content string, age time.Duration) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("writeFileAged: MkdirAll(%q): %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFileAged: WriteFile(%q): %v", path, err)
	}

	now := time.Now()
	mtime := now.Add(-age)
	if err := os.Chtimes(path, now, mtime); err != nil {
		t.Fatalf("writeFileAged: Chtimes(%q): %v", path, err)
	}
}

// mustNewLocalDiskStorage calls NewLocalDiskStorage and fails the test on error.
func mustNewLocalDiskStorage(t *testing.T, root string) *services.LocalDiskStorage {
	t.Helper()

	s, err := services.NewLocalDiskStorage(root)
	if err != nil {
		t.Fatalf("NewLocalDiskStorage(%q): unexpected error: %v", root, err)
	}

	return s
}

// TestLocalDiskStorage_LocalPath_AlwaysAbsolute verifies that LocalPath returns
// an absolute path even when the storage was constructed with a relative root —
// paths cross service boundaries (Go → Python) and must not depend on the caller's CWD.
func TestLocalDiskStorage_LocalPath_AlwaysAbsolute(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "relative root resolves to absolute LocalPath"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("os.Getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(t.TempDir()); err != nil {
				t.Fatalf("os.Chdir: %v", err)
			}

			s := mustNewLocalDiskStorage(t, "./relroot")

			got, err := s.LocalPath(context.Background(), "dQw4w9WgXcQ", "preview.mp3")
			if err != nil {
				t.Fatalf("LocalPath: unexpected error: %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("LocalPath = %q: want absolute path", got)
			}
		})
	}
}

// TestLocalDiskStorage_LocalPath verifies that LocalPath returns a pure
// path derived from root+videoID+name without performing any I/O.
func TestLocalDiskStorage_LocalPath(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name       string
		videoID    string
		file       string
		wantSuffix string
	}{
		{
			name:       "simple",
			videoID:    "dQw4w9WgXcQ",
			file:       "preview.mp3",
			wantSuffix: "dQw4w9WgXcQ/preview.mp3",
		},
		{
			name:       "nested subdir in name",
			videoID:    "dQw4w9WgXcQ",
			file:       "preview-shifts/-3.mp3",
			wantSuffix: "dQw4w9WgXcQ/preview-shifts/-3.mp3",
		},
	}

	s := mustNewLocalDiskStorage(t, root)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.LocalPath(ctx, tt.videoID, tt.file)
			if err != nil {
				t.Fatalf("LocalPath(%q, %q): unexpected error: %v", tt.videoID, tt.file, err)
			}

			wantFull := filepath.Join(root, tt.wantSuffix)
			if got != wantFull {
				t.Errorf("LocalPath(%q, %q) = %q, want %q", tt.videoID, tt.file, got, wantFull)
			}

			if !strings.HasPrefix(got, root) {
				t.Errorf("LocalPath(%q, %q) = %q: does not have prefix %q", tt.videoID, tt.file, got, root)
			}

			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("LocalPath(%q, %q) = %q: does not have suffix %q", tt.videoID, tt.file, got, tt.wantSuffix)
			}
		})
	}
}

// TestLocalDiskStorage_Has verifies that Has returns true iff the file exists
// AND has non-zero size. Old files (well past any previous TTL) are still
// reported present — cache is permanent.
func TestLocalDiskStorage_Has(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		create  bool
		content string
		age     time.Duration
		want    bool
	}{
		{
			name:    "exists with content",
			create:  true,
			content: "audio data",
			age:     0,
			want:    true,
		},
		{
			name:    "exists but zero-byte (corrupt cache entry)",
			create:  true,
			content: "",
			age:     0,
			want:    false,
		},
		{
			name:    "old file well past previous TTL is still present (permanent cache)",
			create:  true,
			content: "audio data",
			age:     30 * 24 * time.Hour, // 30 days ago
			want:    true,
		},
		{
			name:   "does not exist",
			create: false,
			want:   false,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := mustNewLocalDiskStorage(t, root)

			videoID := fmt.Sprintf("videoHas%04d", i)
			name := "preview.mp3"

			if tt.create {
				path, err := s.LocalPath(ctx, videoID, name)
				if err != nil {
					t.Fatalf("LocalPath: %v", err)
				}
				writeFileAged(t, path, tt.content, tt.age)
			}

			got, err := s.Has(ctx, videoID, name)
			if err != nil {
				t.Errorf("Has(%q, %q): unexpected error: %v", videoID, name, err)
			}

			if got != tt.want {
				t.Errorf("Has(%q, %q) = %v, want %v", videoID, name, got, tt.want)
			}
		})
	}
}

// TestLocalDiskStorage_Commit verifies that Commit places a file into the cache
// at the expected path, handles the no-op in-place case, and creates parent
// directories when needed.
func TestLocalDiskStorage_Commit(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
	}{
		{name: "commit moves external file into cache"},
		{name: "commit no-op when file already at target path"},
		{name: "commit creates parent dirs"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := mustNewLocalDiskStorage(t, root)

			videoID := fmt.Sprintf("videoCommit%04d", i)
			const wantContent = "audio bytes"

			switch tt.name {
			case "commit moves external file into cache":
				// Write source file in a completely separate temp dir.
				srcDir := t.TempDir()
				srcPath := filepath.Join(srcDir, "audio.mp3")
				if err := os.WriteFile(srcPath, []byte(wantContent), 0o644); err != nil {
					t.Fatalf("WriteFile src: %v", err)
				}

				target, err := s.LocalPath(ctx, videoID, "preview.mp3")
				if err != nil {
					t.Fatalf("LocalPath: %v", err)
				}

				if err := s.Commit(ctx, videoID, "preview.mp3", srcPath); err != nil {
					t.Fatalf("Commit: unexpected error: %v", err)
				}

				// Target must exist with the right content.
				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("ReadFile target after Commit: %v", err)
				}
				if string(got) != wantContent {
					t.Errorf("target content = %q, want %q", string(got), wantContent)
				}

				// Source must be gone (renamed, not copied).
				if _, err := os.Stat(srcPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("source file still exists at %q after Commit (expected rename)", srcPath)
				}

			case "commit no-op when file already at target path":
				target, err := s.LocalPath(ctx, videoID, "preview.mp3")
				if err != nil {
					t.Fatalf("LocalPath: %v", err)
				}

				// Pre-create the file at the target location.
				writeFileAged(t, target, wantContent, 0)

				// Commit with localPath == target must not return an error.
				if err := s.Commit(ctx, videoID, "preview.mp3", target); err != nil {
					t.Fatalf("Commit (no-op): unexpected error: %v", err)
				}

				// File must still be there with correct content.
				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("ReadFile after no-op Commit: %v", err)
				}
				if string(got) != wantContent {
					t.Errorf("target content after no-op Commit = %q, want %q", string(got), wantContent)
				}

			case "commit creates parent dirs":
				// Use a videoID whose parent dir has not been created yet.
				srcDir := t.TempDir()
				srcPath := filepath.Join(srcDir, "audio.mp3")
				if err := os.WriteFile(srcPath, []byte(wantContent), 0o644); err != nil {
					t.Fatalf("WriteFile src: %v", err)
				}

				target, err := s.LocalPath(ctx, videoID, "stems/vocals.mp3")
				if err != nil {
					t.Fatalf("LocalPath: %v", err)
				}

				// Parent dir must NOT exist yet — verify assumption.
				parentDir := filepath.Dir(target)
				if _, err := os.Stat(parentDir); err == nil {
					t.Fatalf("precondition failed: parent dir %q already exists", parentDir)
				}

				if err := s.Commit(ctx, videoID, "stems/vocals.mp3", srcPath); err != nil {
					t.Fatalf("Commit (creates parent): unexpected error: %v", err)
				}

				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("ReadFile target after Commit: %v", err)
				}
				if string(got) != wantContent {
					t.Errorf("target content = %q, want %q", string(got), wantContent)
				}
			}
		})
	}
}

// TestLocalDiskStorage_Open verifies that Open returns a valid reader for
// existing files and os.ErrNotExist-wrapped errors for missing or zero-byte files.
func TestLocalDiskStorage_Open(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		create            bool
		content           string
		wantErrIsNotExist bool
	}{
		{
			name:              "open existing file returns reader with correct content",
			create:            true,
			content:           "hello",
			wantErrIsNotExist: false,
		},
		{
			name:              "open missing file returns os.ErrNotExist",
			create:            false,
			wantErrIsNotExist: true,
		},
		{
			name:              "open zero-byte file returns os.ErrNotExist (corrupt cache)",
			create:            true,
			content:           "",
			wantErrIsNotExist: true,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := mustNewLocalDiskStorage(t, root)

			videoID := fmt.Sprintf("videoOpen%04d", i)
			name := "preview.mp3"

			if tt.create {
				path, err := s.LocalPath(ctx, videoID, name)
				if err != nil {
					t.Fatalf("LocalPath: %v", err)
				}
				writeFileAged(t, path, tt.content, 0)
			}

			rc, err := s.Open(ctx, videoID, name)

			if tt.wantErrIsNotExist {
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Open(%q, %q): got err = %v, want errors.Is(err, os.ErrNotExist) = true", videoID, name, err)
				}
				if rc != nil {
					_ = rc.Close()
					t.Errorf("Open(%q, %q): got non-nil ReadCloser on error, want nil", videoID, name)
				}
				return
			}

			// Success path.
			if err != nil {
				t.Fatalf("Open(%q, %q): unexpected error: %v", videoID, name, err)
			}
			if rc == nil {
				t.Fatalf("Open(%q, %q): got nil ReadCloser, want non-nil", videoID, name)
			}
			defer func() { _ = rc.Close() }()

			raw, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll from Open reader: %v", err)
			}
			if string(raw) != tt.content {
				t.Errorf("Open(%q, %q) content = %q, want %q", videoID, name, string(raw), tt.content)
			}
		})
	}
}
