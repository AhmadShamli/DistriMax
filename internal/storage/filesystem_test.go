package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestFilesystemStorageLifecycle(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	artifactRoot := filepath.Join(tmpDir, "artifacts")
	stagingRoot := filepath.Join(tmpDir, "staging")

	fs, err := NewFilesystemStorage(artifactRoot, stagingRoot)
	if err != nil {
		t.Fatalf("failed to initialize FilesystemStorage: %v", err)
	}

	content := []byte("mock binary mmdb database content")
	product := "geolite-city"
	version := "2026-09-04"
	filename := "GeoLite2-City.mmdb"

	// 1. Store artifact
	storagePath, err := fs.StoreArtifact(ctx, product, version, filename, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}

	expectedRelPath := filepath.Join(product, version, filename)
	if storagePath != expectedRelPath {
		t.Errorf("expected storagePath %s, got %s", expectedRelPath, storagePath)
	}

	// 2. Verify artifact
	if err := fs.VerifyArtifact(ctx, storagePath, int64(len(content))); err != nil {
		t.Errorf("VerifyArtifact failed: %v", err)
	}

	// Verify size mismatch
	if err := fs.VerifyArtifact(ctx, storagePath, 9999); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("expected ErrSizeMismatch, got %v", err)
	}

	// 3. Open artifact & Read/Seek
	rc, size, err := fs.OpenArtifact(ctx, storagePath)
	if err != nil {
		t.Fatalf("OpenArtifact failed: %v", err)
	}
	defer rc.Close()

	if size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), size)
	}

	readBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readBytes, content) {
		t.Errorf("content mismatch: got %q, want %q", readBytes, content)
	}

	// Test Seek
	_, err = rc.Seek(5, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	buf := make([]byte, 6)
	_, err = io.ReadFull(rc, buf)
	if err != nil {
		t.Fatalf("ReadFull after seek failed: %v", err)
	}
	if string(buf) != "binary" {
		t.Errorf("expected 'binary', got %q", string(buf))
	}

	// 4. Delete artifact
	if err := fs.DeleteArtifact(ctx, storagePath); err != nil {
		t.Fatalf("DeleteArtifact failed: %v", err)
	}

	// Verify not found after delete
	if _, _, err := fs.OpenArtifact(ctx, storagePath); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestFilesystemStoragePathTraversal(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	fs, _ := NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))

	// Traversal in store
	_, err := fs.StoreArtifact(ctx, "../escape", "v1", "file.mmdb", bytes.NewReader([]byte("foo")), 3)
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}

	// Traversal in open
	_, _, err = fs.OpenArtifact(ctx, "../../etc/passwd")
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
}
