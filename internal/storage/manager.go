package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// StorageManager implements StorageBackend while supporting dynamic runtime switching
// between storage backend drivers (e.g. Local Filesystem and S3-compatible object storage)
// configured via the Admin UI.
type StorageManager struct {
	mu           sync.RWMutex
	activeDriver string
	fsStorage    StorageBackend
	s3Storage    StorageBackend
}

// NewStorageManager initializes a manager with the default local filesystem storage.
func NewStorageManager(fsStorage StorageBackend) *StorageManager {
	return &StorageManager{
		activeDriver: "filesystem",
		fsStorage:    fsStorage,
	}
}

// SetS3Storage registers or updates the S3 storage backend instance.
func (m *StorageManager) SetS3Storage(s3 StorageBackend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s3Storage = s3
}

// SetActiveDriver switches the active driver ("filesystem" or "s3").
// If "s3" is requested but no S3 storage is configured, it returns an error.
func (m *StorageManager) SetActiveDriver(driver string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch driver {
	case "s3":
		if m.s3Storage == nil {
			return errors.New("s3 storage backend is not configured")
		}
		m.activeDriver = "s3"
		return nil
	case "filesystem", "":
		m.activeDriver = "filesystem"
		return nil
	default:
		return fmt.Errorf("unknown storage driver: %s", driver)
	}
}

// ActiveDriver returns the currently active storage driver name ("filesystem" or "s3").
func (m *StorageManager) ActiveDriver() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activeDriver == "" {
		return "filesystem"
	}
	return m.activeDriver
}

func (m *StorageManager) currentBackends() (active StorageBackend, fallback StorageBackend) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.activeDriver == "s3" && m.s3Storage != nil {
		return m.s3Storage, m.fsStorage
	}
	return m.fsStorage, m.s3Storage
}

// StoreArtifact stores new artifacts using the currently active backend driver.
func (m *StorageManager) StoreArtifact(ctx context.Context, product, version, filename string, r io.Reader, size int64) (string, error) {
	active, _ := m.currentBackends()
	if active == nil {
		return "", errors.New("no active storage backend available")
	}
	return active.StoreArtifact(ctx, product, version, filename, r, size)
}

// OpenArtifact retrieves the artifact from the active backend, falling back to secondary if not found.
func (m *StorageManager) OpenArtifact(ctx context.Context, storagePath string) (io.ReadSeekCloser, int64, error) {
	active, fallback := m.currentBackends()
	if active == nil {
		return nil, 0, errors.New("no active storage backend available")
	}

	stream, size, err := active.OpenArtifact(ctx, storagePath)
	if err == nil {
		return stream, size, nil
	}

	// If not found on active driver, check fallback driver
	if errors.Is(err, ErrNotFound) && fallback != nil {
		return fallback.OpenArtifact(ctx, storagePath)
	}

	return nil, 0, err
}

// DeleteArtifact deletes the artifact on active backend, and on fallback backend if present.
func (m *StorageManager) DeleteArtifact(ctx context.Context, storagePath string) error {
	active, fallback := m.currentBackends()
	var firstErr error
	if active != nil {
		firstErr = active.DeleteArtifact(ctx, storagePath)
	}
	if fallback != nil {
		_ = fallback.DeleteArtifact(ctx, storagePath)
	}
	return firstErr
}

// VerifyArtifact checks that the stored artifact exists on active or fallback backend.
func (m *StorageManager) VerifyArtifact(ctx context.Context, storagePath string, expectedSize int64) error {
	active, fallback := m.currentBackends()
	if active == nil {
		return errors.New("no active storage backend available")
	}

	err := active.VerifyArtifact(ctx, storagePath, expectedSize)
	if err == nil {
		return nil
	}

	if errors.Is(err, ErrNotFound) && fallback != nil {
		return fallback.VerifyArtifact(ctx, storagePath, expectedSize)
	}

	return err
}
