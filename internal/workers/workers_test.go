package workers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

func TestCleanupWorker(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	database, err := db.Open(filepath.Join(tmpDir, "cleanup.sqlite3"))
	if err != nil {
		t.Fatalf("db open failed: %v", err)
	}
	defer database.Close()
	_ = db.RunMigrations(ctx, database.DB)

	store, _ := storage.NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))

	// 1. Create a version that has already passed its cleanup deadline
	expiredTime := time.Now().UTC().Add(-24 * time.Hour)
	storePath, _ := store.StoreArtifact(ctx, "geolite-city", "expired-v1", "GeoLite2-City.mmdb", bytes.NewReader([]byte("data")), 4)

	v := &db.ProductVersion{
		ID:              "geolite-city:expired-v1",
		ProductID:       "geolite-city",
		Version:         "expired-v1",
		ReleasedAt:      time.Now().UTC().Add(-60 * 24 * time.Hour),
		SHA256:          "expired-sha",
		SizeBytes:       4,
		StorageBackend:  "filesystem",
		StoragePath:     storePath,
		IsCurrent:       false,
		SupersededAt:    &expiredTime,
		CleanupDeadline: &expiredTime,
	}
	query := `INSERT INTO product_versions (id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, superseded_at, cleanup_deadline, is_deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 0)`
	_, _ = database.ExecContext(ctx, query, v.ID, v.ProductID, v.Version, v.ReleasedAt, v.SHA256, v.SizeBytes, v.StorageBackend, v.StoragePath, v.SupersededAt, v.CleanupDeadline)

	// 2. Insert an old audit log
	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)
	_ = database.RecordAuditLog(ctx, &db.AuditLog{
		Timestamp: oldTime,
		SourceIP:  "127.0.0.1",
		Method:    "GET",
		Path:      "/test",
		Status:    200,
	})

	// 3. Run Cleanup
	result, err := RunCleanup(ctx, database, store)
	if err != nil {
		t.Fatalf("RunCleanup failed: %v", err)
	}

	if result.PurgedVersions != 1 {
		t.Errorf("expected 1 purged version, got %d", result.PurgedVersions)
	}
	if result.PurgedAuditLogs != 1 {
		t.Errorf("expected 1 purged audit log, got %d", result.PurgedAuditLogs)
	}

	// Verify artifact is gone from storage
	if _, _, err := store.OpenArtifact(ctx, storePath); err == nil {
		t.Error("expected artifact to be deleted from storage")
	}
}

func TestWebhookSigningAndDelivery(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	database, _ := db.Open(filepath.Join(tmpDir, "wh.sqlite3"))
	defer database.Close()
	_ = db.RunMigrations(ctx, database.DB)

	hmacSecret := "my-very-secret-signing-key"
	received := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		sig := r.Header.Get("DistriMax-Signature")
		if sig == "" {
			t.Error("missing DistriMax-Signature header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Verify signature format t=...,v1=...
		parts := strings.Split(sig, ",")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
			t.Errorf("invalid signature header format: %s", sig)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	event := &WebhookEvent{
		Event:        "database.updated",
		Product:      "geolite-city",
		Version:      "2026-09-04",
		SHA256:       "sha123",
		SizeBytes:    75000000,
		DownloadPath: "/v1/products/geolite-city/download",
	}

	err := SendWebhook(ctx, database, server.URL, hmacSecret, event)
	if err != nil {
		t.Fatalf("SendWebhook failed: %v", err)
	}
	if !received {
		t.Error("webhook receiver was not reached")
	}
}

func TestBackupCreationAndRotation(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	database, _ := db.Open(filepath.Join(tmpDir, "backup_test.sqlite3"))
	defer database.Close()
	_ = db.RunMigrations(ctx, database.DB)

	backupDir := filepath.Join(tmpDir, "backups")

	// Create 4 dummy old backup snapshots
	for i := 1; i <= 4; i++ {
		name := filepath.Join(backupDir, fmt.Sprintf("distrimax-2026090%d-000000.sqlite3", i))
		_ = os.MkdirAll(backupDir, 0750)
		_ = os.WriteFile(name, []byte("snapshot"), 0640)
	}

	// Create new backup with retention = 3
	snapshotPath, err := CreateBackup(ctx, database, backupDir, nil, 3)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	if _, err := os.Stat(snapshotPath); err != nil {
		t.Errorf("expected backup snapshot at %s, got %v", snapshotPath, err)
	}

	// Verify only 3 snapshots remain in directory
	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 3 {
		t.Errorf("expected 3 retained snapshots, found %d", len(entries))
	}
}
