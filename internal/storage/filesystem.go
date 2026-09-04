package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FilesystemStorage implements StorageBackend on local persistent disk.
type FilesystemStorage struct {
	artifactRoot string
	stagingRoot  string
	mu           sync.RWMutex
}

// NewFilesystemStorage initializes and ensures the filesystem directories exist.
func NewFilesystemStorage(artifactRoot, stagingRoot string) (*FilesystemStorage, error) {
	if err := os.MkdirAll(artifactRoot, 0750); err != nil {
		return nil, fmt.Errorf("failed to create artifact root: %w", err)
	}
	if err := os.MkdirAll(stagingRoot, 0750); err != nil {
		return nil, fmt.Errorf("failed to create staging root: %w", err)
	}
	return &FilesystemStorage{
		artifactRoot: artifactRoot,
		stagingRoot:  stagingRoot,
	}, nil
}

// StoreArtifact streams into a staging file and renames it atomically to destination.
func (fs *FilesystemStorage) StoreArtifact(ctx context.Context, product, version, filename string, r io.Reader, size int64) (string, error) {
	if err := validateIdentifiers(product, version, filename); err != nil {
		return "", err
	}

	// 1. Prepare unique staging directory
	stagingID := fmt.Sprintf("%s-%s-%d", product, version, time.Now().UnixNano())
	stagingDir := filepath.Join(fs.stagingRoot, stagingID)
	if err := os.MkdirAll(stagingDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		// Clean up staging directory if not already moved
		_ = os.RemoveAll(stagingDir)
	}()

	stagingFile := filepath.Join(stagingDir, filename)
	f, err := os.OpenFile(stagingFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return "", fmt.Errorf("failed to open staging file: %w", err)
	}

	written, err := io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("failed to write staging file: %w", err)
	}

	if size > 0 && written != size {
		return "", fmt.Errorf("%w: expected %d bytes, got %d bytes", ErrSizeMismatch, size, written)
	}

	// 2. Prepare permanent destination directory
	relPath := filepath.Join(product, version, filename)
	destDir := filepath.Join(fs.artifactRoot, product, version)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	destFile := filepath.Join(destDir, filename)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Atomic rename across POSIX filesystem
	if err := os.Rename(stagingFile, destFile); err != nil {
		return "", fmt.Errorf("failed to move artifact to destination: %w", err)
	}

	return relPath, nil
}

// OpenArtifact opens the artifact file for reading.
func (fs *FilesystemStorage) OpenArtifact(ctx context.Context, storagePath string) (io.ReadSeekCloser, int64, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath, err := fs.resolveSafePath(storagePath)
	if err != nil {
		return nil, 0, err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, err
	}

	return f, info.Size(), nil
}

// DeleteArtifact removes the artifact and any empty parent directories.
func (fs *FilesystemStorage) DeleteArtifact(ctx context.Context, storagePath string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fullPath, err := fs.resolveSafePath(storagePath)
	if err != nil {
		return err
	}

	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete artifact file: %w", err)
	}

	// Clean up empty version directory
	versionDir := filepath.Dir(fullPath)
	_ = os.Remove(versionDir) // Only succeeds if directory is empty

	return nil
}

// VerifyArtifact verifies the file exists on disk and matches expected size.
func (fs *FilesystemStorage) VerifyArtifact(ctx context.Context, storagePath string, expectedSize int64) error {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fullPath, err := fs.resolveSafePath(storagePath)
	if err != nil {
		return err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}

	if expectedSize > 0 && info.Size() != expectedSize {
		return fmt.Errorf("%w: expected %d bytes, found %d bytes", ErrSizeMismatch, expectedSize, info.Size())
	}

	return nil
}

func (fs *FilesystemStorage) resolveSafePath(storagePath string) (string, error) {
	clean := filepath.Clean(storagePath)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", ErrInvalidPath
	}
	return filepath.Join(fs.artifactRoot, clean), nil
}

func validateIdentifiers(parts ...string) error {
	for _, p := range parts {
		if p == "" || strings.Contains(p, "..") || strings.Contains(p, "/") || strings.Contains(p, "\\") {
			return fmt.Errorf("%w: invalid component %q", ErrInvalidPath, p)
		}
	}
	return nil
}
