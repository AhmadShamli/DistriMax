package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDBOpenAndMigrations(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := RunMigrations(ctx, database.DB); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	// Verify idempotency
	if err := RunMigrations(ctx, database.DB); err != nil {
		t.Fatalf("Second RunMigrations failed: %v", err)
	}

	// Verify seeded products
	var count int
	err = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM products").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query products: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 seeded products, got %d", count)
	}

	// Verify baseline settings
	var setupCompleted string
	err = database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'setup_completed'").Scan(&setupCompleted)
	if err != nil {
		t.Fatalf("failed to query setup_completed setting: %v", err)
	}
	if setupCompleted != "false" {
		t.Errorf("expected setup_completed to be 'false', got %s", setupCompleted)
	}

	// Verify migration 2 settings
	var cronVal, stalenessVal string
	if err := database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'sync_schedule_cron'").Scan(&cronVal); err != nil {
		t.Fatalf("failed to query sync_schedule_cron setting: %v", err)
	}
	if cronVal != "0 4 * * *" {
		t.Errorf("expected sync_schedule_cron '0 4 * * *', got %s", cronVal)
	}
	if err := database.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'staleness_threshold_days'").Scan(&stalenessVal); err != nil {
		t.Fatalf("failed to query staleness_threshold_days setting: %v", err)
	}
	if stalenessVal != "8" {
		t.Errorf("expected staleness_threshold_days '8', got %s", stalenessVal)
	}

	var migCount int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migCount); err != nil {
		t.Fatalf("failed to count schema_migrations: %v", err)
	}
	if migCount != 3 {
		t.Errorf("expected 3 applied schema migrations, got %d", migCount)
	}
}

func TestDBTransactions(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "tx_test.sqlite3")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	_ = RunMigrations(ctx, database.DB)

	// Test rollback on error
	errRollback := errors.New("deliberate rollback")
	err = database.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO settings (key, value, updated_by) VALUES ('test_key', 'val', 'tester')")
		if err != nil {
			return err
		}
		return errRollback
	})

	if !errors.Is(err, errRollback) {
		t.Errorf("expected %v, got %v", errRollback, err)
	}

	var count int
	_ = database.QueryRow("SELECT COUNT(*) FROM settings WHERE key = 'test_key'").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after rollback, got %d", count)
	}

	// Test commit
	err = database.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO settings (key, value, updated_by) VALUES ('test_key', 'val', 'tester')")
		return err
	})
	if err != nil {
		t.Fatalf("commit transaction failed: %v", err)
	}

	_ = database.QueryRow("SELECT COUNT(*) FROM settings WHERE key = 'test_key'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after commit, got %d", count)
	}
}

func TestDBVacuumIntoBackup(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "primary.sqlite3")
	backupPath := filepath.Join(tmpDir, "backups", "snapshot.sqlite3")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	_ = RunMigrations(ctx, database.DB)

	if err := database.VacuumInto(ctx, backupPath); err != nil {
		t.Fatalf("VacuumInto failed: %v", err)
	}

	// Verify the backup snapshot can be opened and queried
	backupDB, err := Open(backupPath)
	if err != nil {
		t.Fatalf("failed to open backup db: %v", err)
	}
	defer backupDB.Close()

	var count int
	if err := backupDB.QueryRow("SELECT COUNT(*) FROM products").Scan(&count); err != nil {
		t.Fatalf("failed to query backup db: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 products in backup snapshot, got %d", count)
	}
}

func TestProductAndVersionLifecycle(t *testing.T) {
	ctx := context.Background()
	database, _ := Open(filepath.Join(t.TempDir(), "prod.sqlite3"))
	defer database.Close()
	_ = RunMigrations(ctx, database.DB)

	p, err := database.GetProduct(ctx, "geolite-city")
	if err != nil {
		t.Fatalf("GetProduct failed: %v", err)
	}
	if p.EditionID != "GeoLite2-City" {
		t.Errorf("expected GeoLite2-City, got %s", p.EditionID)
	}

	// Publish Version 1
	v1 := &ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-01",
		ReleasedAt:     time.Now().UTC().Add(-48 * time.Hour),
		SHA256:         "sha256-v1",
		SizeBytes:      1000,
		StorageBackend: "filesystem",
		StoragePath:    "geolite-city/2026-09-01/GeoLite2-City.mmdb",
	}
	if err := database.PublishNewVersion(ctx, v1, 30); err != nil {
		t.Fatalf("PublishNewVersion v1 failed: %v", err)
	}

	current, err := database.GetCurrentVersion(ctx, "geolite-city")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	if current.Version != "2026-09-01" {
		t.Errorf("expected 2026-09-01, got %s", current.Version)
	}

	// Publish Version 2
	v2 := &ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-04",
		ReleasedAt:     time.Now().UTC(),
		SHA256:         "sha256-v2",
		SizeBytes:      2000,
		StorageBackend: "filesystem",
		StoragePath:    "geolite-city/2026-09-04/GeoLite2-City.mmdb",
	}
	if err := database.PublishNewVersion(ctx, v2, 30); err != nil {
		t.Fatalf("PublishNewVersion v2 failed: %v", err)
	}

	current, _ = database.GetCurrentVersion(ctx, "geolite-city")
	if current.Version != "2026-09-04" {
		t.Errorf("expected current 2026-09-04, got %s", current.Version)
	}

	// Check historical versions list
	versions, err := database.ListProductVersions(ctx, "geolite-city")
	if err != nil {
		t.Fatalf("ListProductVersions failed: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("expected 2 versions, got %d", len(versions))
	}

	// Test Rollback to Version 1
	if err := database.RollbackToVersion(ctx, "geolite-city", "2026-09-01", 30); err != nil {
		t.Fatalf("RollbackToVersion failed: %v", err)
	}

	current, _ = database.GetCurrentVersion(ctx, "geolite-city")
	if current.Version != "2026-09-01" {
		t.Errorf("expected rollback to 2026-09-01, got %s", current.Version)
	}
}

func TestUsersAndSessionsSecurity(t *testing.T) {
	ctx := context.Background()
	database, _ := Open(filepath.Join(t.TempDir(), "users.sqlite3"))
	defer database.Close()
	_ = RunMigrations(ctx, database.DB)

	// Create user
	u1, err := database.CreateUser(ctx, "admin", "hash123")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Test disabling the only admin (should fail)
	err = database.SetUserDisabled(ctx, u1.ID, true)
	if !errors.Is(err, ErrCannotDisableLastAdmin) {
		t.Errorf("expected ErrCannotDisableLastAdmin, got %v", err)
	}

	// Create second admin
	u2, err := database.CreateUser(ctx, "admin2", "hash456")
	if err != nil {
		t.Fatalf("CreateUser admin2 failed: %v", err)
	}

	// Now disabling u1 should succeed because u2 is active
	err = database.SetUserDisabled(ctx, u1.ID, true)
	if err != nil {
		t.Errorf("expected success disabling u1, got %v", err)
	}

	// Sessions
	sessionID := "sess-token-123"
	if err := database.CreateSession(ctx, sessionID, u2.ID, time.Now().UTC().Add(1*time.Hour)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	s, user, err := database.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if user.Username != "admin2" || s.UserID != u2.ID {
		t.Errorf("user/session mismatch: %s vs %s", user.Username, u2.ID)
	}

	// Revoke
	if err := database.RevokeSession(ctx, sessionID); err != nil {
		t.Fatalf("RevokeSession failed: %v", err)
	}
	if _, _, err := database.GetSession(ctx, sessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound after revoke, got %v", err)
	}
}

func TestAPIKeysAndAudit(t *testing.T) {
	ctx := context.Background()
	database, _ := Open(filepath.Join(t.TempDir(), "api_audit.sqlite3"))
	defer database.Close()
	_ = RunMigrations(ctx, database.DB)

	key := &APIKey{
		KeyPrefix:       "dm_live_abcd",
		KeyHash:         "argon2hash123",
		DisplayName:     "K8s Ingress",
		Scopes:          "manifest:read,download",
		AllowedProducts: "*",
		RateLimitPerMin: 60,
	}
	if err := database.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}

	keys, err := database.GetAPIKeysByPrefix(ctx, "dm_live_abcd")
	if err != nil {
		t.Fatalf("GetAPIKeysByPrefix failed: %v", err)
	}
	if len(keys) != 1 || keys[0].DisplayName != "K8s Ingress" {
		t.Errorf("unexpected keys: %+v", keys)
	}

	// Audit log
	prod := "geolite-city"
	ver := "2026-09-04"
	log := &AuditLog{
		SourceIP:   "10.0.1.2",
		Method:     "GET",
		Path:       "/v1/products/geolite-city/download",
		Status:     200,
		DurationMs: 15,
		BytesSent:  75000000,
		APIKeyID:   &key.ID,
		ProductID:  &prod,
		Version:    &ver,
		UserAgent:  "curl/7.88",
	}
	if err := database.RecordAuditLog(ctx, log); err != nil {
		t.Fatalf("RecordAuditLog failed: %v", err)
	}

	stats, err := database.GetDownloadStats(ctx, 24)
	if err != nil {
		t.Fatalf("GetDownloadStats failed: %v", err)
	}
	if stats.TotalRequests != 1 || stats.BytesTransferred != 75000000 {
		t.Errorf("unexpected stats: %+v", stats)
	}
}

func TestDownloadTokensAndProductVersions(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "tokens_test.sqlite3")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
	_ = RunMigrations(ctx, database.DB)

	// Test GetProductVersion
	pv := &ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-01",
		ReleasedAt:     time.Now().UTC(),
		SHA256:         "sha256-test-1",
		SizeBytes:      1024,
		StorageBackend: "filesystem",
		StoragePath:    "artifacts/geolite-city/2026-09-01/GeoLite2-City.mmdb",
	}
	if err := database.PublishNewVersion(ctx, pv, 30); err != nil {
		t.Fatalf("PublishNewVersion failed: %v", err)
	}

	fetched, err := database.GetProductVersion(ctx, "geolite-city", "2026-09-01")
	if err != nil {
		t.Fatalf("GetProductVersion failed: %v", err)
	}
	if fetched.Version != "2026-09-01" || fetched.SHA256 != "sha256-test-1" {
		t.Errorf("unexpected product version: %+v", fetched)
	}

	// Non-existent version
	_, err = database.GetProductVersion(ctx, "geolite-city", "non-existent")
	if err == nil {
		t.Error("expected error for non-existent version, got nil")
	}

	// Test Download Tokens CRUD
	expires := time.Now().UTC().Add(24 * time.Hour)
	token := &DownloadToken{
		Token:     "dmt_testtoken12345",
		ProductID: "geolite-city",
		Version:   "2026-09-01",
		CreatedBy: "admin",
		ExpiresAt: expires,
	}
	if err := database.CreateDownloadToken(ctx, token); err != nil {
		t.Fatalf("CreateDownloadToken failed: %v", err)
	}

	gotToken, err := database.GetDownloadToken(ctx, "dmt_testtoken12345")
	if err != nil {
		t.Fatalf("GetDownloadToken failed: %v", err)
	}
	if gotToken.ProductID != "geolite-city" || gotToken.Version != "2026-09-01" || gotToken.DownloadCount != 0 || gotToken.IsRevoked {
		t.Errorf("unexpected token state: %+v", gotToken)
	}

	// Increment usage
	if err := database.IncrementDownloadTokenUsage(ctx, "dmt_testtoken12345"); err != nil {
		t.Fatalf("IncrementDownloadTokenUsage failed: %v", err)
	}
	gotToken, _ = database.GetDownloadToken(ctx, "dmt_testtoken12345")
	if gotToken.DownloadCount != 1 {
		t.Errorf("expected download count 1, got %d", gotToken.DownloadCount)
	}

	// Revoke
	if err := database.RevokeDownloadToken(ctx, "dmt_testtoken12345"); err != nil {
		t.Fatalf("RevokeDownloadToken failed: %v", err)
	}
	gotToken, _ = database.GetDownloadToken(ctx, "dmt_testtoken12345")
	if !gotToken.IsRevoked {
		t.Errorf("expected token to be revoked")
	}

	// Cleanup
	cleaned, err := database.CleanExpiredDownloadTokens(ctx)
	if err != nil {
		t.Fatalf("CleanExpiredDownloadTokens failed: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("expected 1 cleaned token, got %d", cleaned)
	}
}
