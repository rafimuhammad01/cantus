// Package services provides shared service types for the cantus backend.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Storage abstracts cache read/write operations so handlers never touch file paths directly.
type Storage interface {
	LocalPath(ctx context.Context, videoID, name string) (string, error)
	Has(ctx context.Context, videoID, name string) (bool, error)
	Commit(ctx context.Context, videoID, name, localPath string) error
	Open(ctx context.Context, videoID, name string) (io.ReadCloser, error)
}

// LocalDiskStorage is a permanent, disk-backed Storage implementation.
// Cache entries are never expired or evicted — stored files persist until
// explicitly deleted from the filesystem (e.g. by an external lifecycle policy).
type LocalDiskStorage struct {
	root string
}

// NewLocalDiskStorage creates a LocalDiskStorage rooted at root.
// It creates root (and any parents) if it does not exist. root is resolved to an
// absolute path so LocalPath results are portable across services with different CWDs.
// Cache entries are permanent: no TTL is enforced and no cleanup goroutine is started.
func NewLocalDiskStorage(root string) (*LocalDiskStorage, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: MkdirAll(%q): %w", root, err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: Abs(%q): %w", root, err)
	}
	return &LocalDiskStorage{root: absRoot}, nil
}

// LocalPath returns the absolute path for (videoID, name) without performing I/O.
func (s *LocalDiskStorage) LocalPath(_ context.Context, videoID, name string) (string, error) {
	return filepath.Join(s.root, videoID, name), nil
}

// Has reports whether (videoID, name) is present in cache with non-zero size.
// A missing file returns (false, nil). A zero-byte file also returns (false, nil)
// so that a corrupt or incomplete cache entry forces regeneration.
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

	return info.Size() > 0, nil
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
// wrapped error when the file is missing or zero-byte (corrupt cache entry).
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
