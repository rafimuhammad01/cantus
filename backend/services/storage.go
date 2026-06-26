// Package services provides shared service types for the cantus backend.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
)

// AudioExt is the on-disk extension for all cached audio artifacts.
// Cache keys must always use this; serving handlers use it for ServeContent names.
const AudioExt = ".mp3"

// ErrObjectNotMaterialized is returned by Verify when the object is absent
// or zero-byte. Used after a Python upload to confirm the PUT actually
// landed before treating the cache entry as ready.
var ErrObjectNotMaterialized = errors.New("storage: object not materialized")

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
	Verify(ctx context.Context, key string) error
}

// LocalDiskStorage is a permanent, disk-backed Storage implementation.
// Cache entries are never expired or evicted — stored files persist until
// explicitly deleted from the filesystem (e.g. by an external lifecycle policy).
type LocalDiskStorage struct {
	root        string
	blobBaseURL string       // empty = SignGet/SignPut return ("", nil)
	tokener     *BlobTokener // nil = SignGet/SignPut return ("", nil)
	ttl         time.Duration
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

// NewLocalDiskStorageWithBlob creates a LocalDiskStorage that can mint /internal/blob URLs.
// The tokener and blobBaseURL are used by SignGet/SignPut to produce short-lived HMAC URLs.
func NewLocalDiskStorageWithBlob(root, blobBaseURL string, tokener *BlobTokener, ttl time.Duration) (*LocalDiskStorage, error) {
	s, err := NewLocalDiskStorage(root)
	if err != nil {
		return nil, err
	}
	s.blobBaseURL = blobBaseURL
	s.tokener = tokener
	s.ttl = ttl
	return s, nil
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

// signURL builds a short-lived HMAC-gated /internal/blob URL for the given key and op.
// Returns ("", nil) when no blob config is present (back-compat with NewLocalDiskStorage).
func (s *LocalDiskStorage) signURL(key, op string) (string, error) {
	if s.tokener == nil || s.blobBaseURL == "" {
		return "", nil
	}
	exp := time.Now().Add(s.ttl)
	token := s.tokener.Sign(key, op, exp)
	return fmt.Sprintf("%s/internal/blob/%s?op=%s&exp=%d&token=%s",
		s.blobBaseURL, key, op, exp.Unix(), token), nil
}

// SignGet returns a presigned GET URL for the given key. Returns ("", nil) when no
// blob config is set (created via NewLocalDiskStorage without blob wiring).
func (s *LocalDiskStorage) SignGet(_ context.Context, key string) (string, error) {
	return s.signURL(key, "get")
}

// SignPut returns a presigned PUT URL for the given key. Returns ("", nil) when no
// blob config is set (created via NewLocalDiskStorage without blob wiring).
func (s *LocalDiskStorage) SignPut(_ context.Context, key string) (string, error) {
	return s.signURL(key, "put")
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

// Verify reports nil if the object exists with non-zero size, otherwise
// ErrObjectNotMaterialized. Used after a Python upload completes (200 OK
// from the processor) to catch the "service returned success but the PUT
// silently failed" failure mode.
func (s *LocalDiskStorage) Verify(ctx context.Context, key string) error {
	ok, err := s.Has(ctx, key)
	if err != nil {
		return fmt.Errorf("storage: Verify %s: %w", key, err)
	}
	if !ok {
		return fmt.Errorf("storage: %s: %w", key, ErrObjectNotMaterialized)
	}
	return nil
}
