package services_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// TestLocalDiskStorage_Key_isPureFunction verifies that Key returns opaque
// forward-slash-joined keys that embed the videoID and object name, without
// performing any I/O.
func TestLocalDiskStorage_Key_isPureFunction(t *testing.T) {
	s, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}

	cases := []struct {
		name       string
		videoID    string
		objectName string
		wantSuffix string
	}{
		{"top-level file", "abc12345678", "melody.json", "abc12345678/melody.json"},
		{"nested path", "abc12345678", "shifted/0/audio.mp3", "abc12345678/shifted/0/audio.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.Key(tc.videoID, tc.objectName)
			if got != tc.wantSuffix {
				t.Errorf("Key(%q, %q) = %q, want %q", tc.videoID, tc.objectName, got, tc.wantSuffix)
			}
		})
	}
}

// TestLocalDiskStorage_HasOpenCommit_byKey verifies the Has/Commit/Open round-trip
// using key-based API (key produced by Key()).
func TestLocalDiskStorage_HasOpenCommit_byKey(t *testing.T) {
	s, err := services.NewLocalDiskStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalDiskStorage: %v", err)
	}
	ctx := context.Background()

	key := s.Key("abc12345678", "melody.json")

	has, err := s.Has(ctx, key)
	if err != nil {
		t.Fatalf("Has (missing): unexpected error: %v", err)
	}
	if has {
		t.Fatalf("Has (missing): got true, want false")
	}

	src := filepath.Join(t.TempDir(), "src.json")
	if err := os.WriteFile(src, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("WriteFile src: %v", err)
	}
	if err := s.Commit(ctx, key, src); err != nil {
		t.Fatalf("Commit: unexpected error: %v", err)
	}

	has, err = s.Has(ctx, key)
	if err != nil {
		t.Fatalf("Has (present): unexpected error: %v", err)
	}
	if !has {
		t.Fatalf("Has (present): got false, want true")
	}

	rc, err := s.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("Open content = %q, want %q", string(body), `{"ok":true}`)
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
			s, err := services.NewLocalDiskStorage(root)
			if err != nil {
				t.Fatalf("NewLocalDiskStorage: %v", err)
			}

			videoID := "videoHas" + string(rune('A'+i))
			key := s.Key(videoID, "preview.mp3")

			if tt.create {
				absPath := s.FilesystemPathForLocalProcessor(key)
				writeFileAged(t, absPath, tt.content, tt.age)
			}

			got, err := s.Has(ctx, key)
			if err != nil {
				t.Errorf("Has(%q): unexpected error: %v", key, err)
			}
			if got != tt.want {
				t.Errorf("Has(%q) = %v, want %v", key, got, tt.want)
			}
		})
	}
}

// TestLocalDiskStorage_Commit verifies that Commit places a file into the cache
// at the expected path, handles the no-op in-place case, and creates parent
// directories when needed.
func TestLocalDiskStorage_Commit(t *testing.T) {
	ctx := context.Background()
	const wantContent = "audio bytes"

	// stageSourceFile writes wantContent to a fresh temp file outside the cache
	// root and returns its path. Used by cases that exercise the Rename path.
	stageSourceFile := func(t *testing.T) string {
		t.Helper()
		srcPath := filepath.Join(t.TempDir(), "audio.mp3")
		if err := os.WriteFile(srcPath, []byte(wantContent), 0o644); err != nil {
			t.Fatalf("WriteFile src: %v", err)
		}
		return srcPath
	}

	tests := []struct {
		name       string
		objectName string
		// setup runs before Commit. Returns (localPath to pass to Commit, srcPath
		// to assert is removed after the rename, or "" to skip the source-removed check).
		setup func(t *testing.T, s *services.LocalDiskStorage, key string) (localPath, srcPath string)
		// extraAssert runs after the post-commit content check, for case-specific assertions.
		extraAssert func(t *testing.T, target, srcPath string)
	}{
		{
			name:       "moves external file into cache",
			objectName: "preview.mp3",
			setup: func(t *testing.T, _ *services.LocalDiskStorage, _ string) (string, string) {
				src := stageSourceFile(t)
				return src, src
			},
			extraAssert: func(t *testing.T, _, srcPath string) {
				if _, err := os.Stat(srcPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("source file still exists at %q after Commit (expected rename)", srcPath)
				}
			},
		},
		{
			name:       "no-op when file already at target path",
			objectName: "preview.mp3",
			setup: func(t *testing.T, s *services.LocalDiskStorage, key string) (string, string) {
				target := s.FilesystemPathForLocalProcessor(key)
				writeFileAged(t, target, wantContent, 0)
				return target, ""
			},
		},
		{
			name:       "creates parent dirs",
			objectName: "stems/vocals.mp3",
			setup: func(t *testing.T, s *services.LocalDiskStorage, key string) (string, string) {
				src := stageSourceFile(t)
				parentDir := filepath.Dir(s.FilesystemPathForLocalProcessor(key))
				if _, err := os.Stat(parentDir); err == nil {
					t.Fatalf("precondition failed: parent dir %q already exists", parentDir)
				}
				return src, src
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s, err := services.NewLocalDiskStorage(root)
			if err != nil {
				t.Fatalf("NewLocalDiskStorage: %v", err)
			}

			videoID := "videoCommit" + string(rune('A'+i))
			key := s.Key(videoID, tt.objectName)

			localPath, srcPath := tt.setup(t, s, key)

			if err := s.Commit(ctx, key, localPath); err != nil {
				t.Fatalf("Commit: unexpected error: %v", err)
			}

			target := s.FilesystemPathForLocalProcessor(key)
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile target after Commit: %v", err)
			}
			if string(got) != wantContent {
				t.Errorf("target content = %q, want %q", string(got), wantContent)
			}

			if tt.extraAssert != nil {
				tt.extraAssert(t, target, srcPath)
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
			s, err := services.NewLocalDiskStorage(root)
			if err != nil {
				t.Fatalf("NewLocalDiskStorage: %v", err)
			}

			videoID := "videoOpen" + string(rune('A'+i))
			key := s.Key(videoID, "preview.mp3")

			if tt.create {
				absPath := s.FilesystemPathForLocalProcessor(key)
				writeFileAged(t, absPath, tt.content, 0)
			}

			rc, err := s.Open(ctx, key)

			if tt.wantErrIsNotExist {
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Open(%q): got err = %v, want errors.Is(err, os.ErrNotExist) = true", key, err)
				}
				if rc != nil {
					_ = rc.Close()
					t.Errorf("Open(%q): got non-nil ReadCloser on error, want nil", key)
				}
				return
			}

			// Success path.
			if err != nil {
				t.Fatalf("Open(%q): unexpected error: %v", key, err)
			}
			if rc == nil {
				t.Fatalf("Open(%q): got nil ReadCloser, want non-nil", key)
			}
			defer func() { _ = rc.Close() }()

			raw, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll from Open reader: %v", err)
			}
			if string(raw) != tt.content {
				t.Errorf("Open(%q) content = %q, want %q", key, string(raw), tt.content)
			}
		})
	}
}
