// Package services provides shared service types for the cantus backend.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
)

// Storage abstracts cache read/write operations so handlers never touch file paths directly.
// Keys are opaque strings produced by Key(videoID, name) — callers must not construct
// keys directly.
type Storage interface {
	// Key returns an opaque cache key for (videoID, name). Pure function: no I/O, no error.
	Key(videoID, name string) string

	Has(ctx context.Context, key string) (bool, error)
	SignGet(ctx context.Context, key string) (string, error)
	SignPut(ctx context.Context, key string) (string, error)
	Commit(ctx context.Context, key, localPath string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// LocalDiskStorage is a permanent, disk-backed Storage implementation.
// Cache entries are never expired or evicted — stored files persist until
// explicitly deleted from the filesystem (e.g. by an external lifecycle policy).
type LocalDiskStorage struct {
	root string
}

// NewLocalDiskStorage creates a LocalDiskStorage rooted at root.
// It creates root (and any parents) if it does not exist. root is resolved to an
// absolute path so paths are portable across services with different CWDs.
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

// Key returns an opaque forward-slash-joined cache key for (videoID, name).
// It is a pure function: no I/O is performed and no error is returned.
func (s *LocalDiskStorage) Key(videoID, name string) string {
	return path.Join(videoID, name)
}

// absPath converts an opaque key back to an absolute filesystem path.
func (s *LocalDiskStorage) absPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

// Has reports whether key is present in cache with non-zero size.
// A missing file returns (false, nil). A zero-byte file also returns (false, nil)
// so that a corrupt or incomplete cache entry forces regeneration.
func (s *LocalDiskStorage) Has(_ context.Context, key string) (bool, error) {
	info, err := os.Stat(s.absPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Size() > 0, nil
}

// SignGet returns a presigned GET URL for the given key. LocalDiskStorage has no
// remote backing store, so it returns ("", nil) as a placeholder. Replaced in Task 4.
func (s *LocalDiskStorage) SignGet(_ context.Context, _ string) (string, error) {
	return "", nil
}

// SignPut returns a presigned PUT URL for the given key. LocalDiskStorage has no
// remote backing store, so it returns ("", nil) as a placeholder. Replaced in Task 4.
func (s *LocalDiskStorage) SignPut(_ context.Context, _ string) (string, error) {
	return "", nil
}

// Commit moves localPath into the cache at key. If localPath is already the
// target location the call is a no-op. Parent directories are created as needed.
func (s *LocalDiskStorage) Commit(_ context.Context, key, localPath string) error {
	target := s.absPath(key)
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

// Open returns a ReadCloser for key. It returns an os.ErrNotExist-wrapped error
// when the file is missing or zero-byte (corrupt cache entry).
func (s *LocalDiskStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	ok, err := s.Has(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("storage: Has %s: %w", key, err)
	}
	if !ok {
		return nil, fmt.Errorf("storage: %s: %w", key, os.ErrNotExist)
	}
	f, err := os.Open(s.absPath(key))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", key, err)
	}
	return f, nil
}

// FilesystemPathForLocalProcessor is a transitional escape hatch for code paths
// that still call the path-based ProcessorClient. Removed in plan #4.
func (s *LocalDiskStorage) FilesystemPathForLocalProcessor(key string) string {
	return s.absPath(key)
}
