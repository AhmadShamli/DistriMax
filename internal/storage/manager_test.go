package storage

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
)

func TestStorageManager(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	fs, err := NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))
	if err != nil {
		t.Fatalf("NewFilesystemStorage failed: %v", err)
	}

	mgr := NewStorageManager(fs)
	if mgr.ActiveDriver() != "filesystem" {
		t.Errorf("expected default driver filesystem, got %s", mgr.ActiveDriver())
	}

	// Store on filesystem
	content := []byte("test mmdb content")
	relPath, err := mgr.StoreArtifact(ctx, "test-prod", "v1", "test.mmdb", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}

	// Read back
	stream, size, err := mgr.OpenArtifact(ctx, relPath)
	if err != nil {
		t.Fatalf("OpenArtifact failed: %v", err)
	}
	defer stream.Close()

	if size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), size)
	}

	readBytes, _ := io.ReadAll(stream)
	if !bytes.Equal(readBytes, content) {
		t.Error("content mismatch")
	}

	// Attempt to set S3 without instance -> error
	if err := mgr.SetActiveDriver("s3"); err == nil {
		t.Error("expected error setting S3 without S3 backend configured")
	}

	// Verify artifact
	if err := mgr.VerifyArtifact(ctx, relPath, int64(len(content))); err != nil {
		t.Errorf("VerifyArtifact failed: %v", err)
	}

	// Delete artifact
	if err := mgr.DeleteArtifact(ctx, relPath); err != nil {
		t.Errorf("DeleteArtifact failed: %v", err)
	}

	// Test filesystem read/write/delete capabilities check
	if err := mgr.CheckFilesystem(ctx); err != nil {
		t.Errorf("CheckFilesystem failed: %v", err)
	}
}

