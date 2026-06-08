// Package services provides shared service types for the cantus backend.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Storage abstracts cache read/write operations so handlers never touch file paths directly.
type Storage interface {
	LocalPath(ctx context.Context, videoID, name string) (string, error)
	Has(ctx context.Context, videoID, name string) (bool, error)
	Commit(ctx context.Context, videoID, name, localPath string) error
	Open(ctx context.Context, videoID, name string) (io.ReadCloser, error)
}

// LocalDiskStorage is a TTL-aware, disk-backed Storage implementation.
type LocalDiskStorage struct {
	root string
	ttl  time.Duration
}

// NewLocalDiskStorage creates a LocalDiskStorage rooted at root with the given TTL.
// It creates root (and any parents) if it does not exist. root is resolved to an
// absolute path so LocalPath results are portable across services with different CWDs.
func NewLocalDiskStorage(root string, ttl time.Duration) (*LocalDiskStorage, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: MkdirAll(%q): %w", root, err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: Abs(%q): %w", root, err)
	}
	return &LocalDiskStorage{root: absRoot, ttl: ttl}, nil
}

// LocalPath returns the absolute path for (videoID, name) without performing I/O.
func (s *LocalDiskStorage) LocalPath(_ context.Context, videoID, name string) (string, error) {
	return filepath.Join(s.root, videoID, name), nil
}

// Has reports whether (videoID, name) is present in cache and within its TTL.
// A stale file returns (false, nil); a missing file returns (false, nil).
func (s *LocalDiskStorage) Has(ctx context.Context, videoID, name string) (bool, error) {
	path, err := s.LocalPath(ctx, videoID, name)
	if err != nil {
		return false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return time.Since(info.ModTime()) <= s.ttl, nil
}

// Commit moves localPath into the cache at (videoID, name). If localPath is
// already the target location the call is a no-op. Parent directories are
// created as needed.
func (s *LocalDiskStorage) Commit(ctx context.Context, videoID, name, localPath string) error {
	target, err := s.LocalPath(ctx, videoID, name)
	if err != nil {
		return err
	}

	if localPath == target {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("storage: MkdirAll(%q): %w", filepath.Dir(target), err)
	}

	if err := os.Rename(localPath, target); err != nil {
		return fmt.Errorf("storage: rename %q -> %q: %w", localPath, target, err)
	}

	return nil
}

// Open returns a ReadCloser for (videoID, name). It returns an os.ErrNotExist-
// wrapped error when the file is missing or stale.
func (s *LocalDiskStorage) Open(ctx context.Context, videoID, name string) (io.ReadCloser, error) {
	ok, err := s.Has(ctx, videoID, name)
	if err != nil {
		return nil, fmt.Errorf("storage: Has %s/%s: %w", videoID, name, err)
	}
	if !ok {
		return nil, fmt.Errorf("storage: %s/%s: %w", videoID, name, os.ErrNotExist)
	}

	path, err := s.LocalPath(ctx, videoID, name)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s/%s: %w", videoID, name, err)
	}

	return f, nil
}

// Cleanup removes all files under root whose mtime exceeds the TTL, then
// removes any empty per-videoID subdirectories. It returns the count of files
// removed and any walk error.
func (s *LocalDiskStorage) Cleanup() (int, error) {
	count := 0

	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil // file vanished between WalkDir and Stat; skip
		}

		if time.Since(info.ModTime()) > s.ttl {
			if removeErr := os.Remove(path); removeErr == nil {
				count++
			}
		}

		return nil
	})

	// Second pass: prune empty videoID subdirectories.
	entries, readErr := os.ReadDir(s.root)
	if readErr == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(s.root, e.Name())
			children, _ := os.ReadDir(dir)
			if len(children) == 0 {
				_ = os.Remove(dir)
			}
		}
	}

	return count, err
}

// StartCleanup launches a background goroutine that calls Cleanup on every
// interval tick until ctx is cancelled.
func (s *LocalDiskStorage) StartCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = s.Cleanup()
			}
		}
	}()
}
