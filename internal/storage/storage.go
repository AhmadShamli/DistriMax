package storage

import (
	"context"
	"errors"
	"io"
)

var (
	ErrNotFound       = errors.New("artifact not found")
	ErrSizeMismatch   = errors.New("artifact size mismatch")
	ErrInvalidPath    = errors.New("invalid storage path")
)

// StorageBackend defines the interface for persisting and serving MMDB artifacts.
type StorageBackend interface {
	// StoreArtifact writes a staging artifact and moves it atomically into its versioned destination.
	// Returns the relative or canonical storagePath identifier.
	StoreArtifact(ctx context.Context, product, version, filename string, r io.Reader, size int64) (storagePath string, err error)

	// OpenArtifact opens the artifact for streaming to an HTTP client.
	// The returned ReadSeekCloser supports Seek for HTTP Range requests.
	OpenArtifact(ctx context.Context, storagePath string) (io.ReadSeekCloser, int64, error)

	// DeleteArtifact purges the artifact and its parent version container when superseded.
	DeleteArtifact(ctx context.Context, storagePath string) error

	// VerifyArtifact checks that the stored artifact exists and matches the expected byte size.
	VerifyArtifact(ctx context.Context, storagePath string, expectedSize int64) error
}
