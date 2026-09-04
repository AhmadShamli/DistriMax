package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/cache"
	"github.com/AhmadShamli/DistriMax/internal/config"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

func setupTestServer(t *testing.T) (*Server, *db.DB, storage.StorageBackend, string, string) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	database, err := db.Open(filepath.Join(tmpDir, "api_test.sqlite3"))
	if err != nil {
		t.Fatalf("db open failed: %v", err)
	}
	_ = db.RunMigrations(ctx, database.DB)

	store, err := storage.NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))
	if err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	manifestCache := cache.NewManifestCache(1 * time.Minute)

	cfg := &config.Config{
		RecoverySecret: "super-secret-recovery-token",
		StorageBackend: "filesystem",
	}

	server := NewServer(cfg, database, store, manifestCache)

	// Publish test version for geolite-city
	mockContent := []byte("binary mmdb content for city database")
	storePath, err := store.StoreArtifact(ctx, "geolite-city", "2026-09-04", "GeoLite2-City.mmdb", bytes.NewReader(mockContent), int64(len(mockContent)))
	if err != nil {
		t.Fatalf("store artifact failed: %v", err)
	}

	v := &db.ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-04",
		ReleasedAt:     time.Now().UTC(),
		SHA256:         "mock-sha256-hash-12345",
		SizeBytes:      int64(len(mockContent)),
		StorageBackend: "filesystem",
		StoragePath:    storePath,
	}
	_ = database.PublishNewVersion(ctx, v, 30)

	// Disable other seeded products for test isolation
	_, _ = database.ExecContext(ctx, "UPDATE products SET is_enabled = 0 WHERE id != 'geolite-city'")

	// Create valid test API key
	secret, prefix, hash, _ := auth.GenerateAPIKey()
	apiKey := &db.APIKey{
		KeyPrefix:       prefix,
		KeyHash:         hash,
		DisplayName:     "Test Key",
		Scopes:          "manifest:read,download",
		AllowedProducts: "*",
		RateLimitPerMin: 60,
	}
	_ = database.CreateAPIKey(ctx, apiKey)

	return server, database, store, secret, cfg.RecoverySecret
}

func TestHealthEndpoints(t *testing.T) {
	server, database, _, _, _ := setupTestServer(t)
	defer database.Close()

	handler := server.Handler()

	// 1. Liveness
	req := httptest.NewRequest(http.MethodGet, "/health/liveness", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("liveness expected 200, got %d", rr.Code)
	}

	// 2. Readiness
	req = httptest.NewRequest(http.MethodGet, "/health/readiness", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("readiness expected 200, got %d", rr.Code)
	}
}

func TestAPIKeyAuthenticationMethods(t *testing.T) {
	server, database, _, apiKey, _ := setupTestServer(t)
	defer database.Close()

	handler := server.Handler()

	// 1. Missing key -> 401
	req := httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/manifest", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing key, got %d", rr.Code)
	}

	// 2. Bearer Header -> 200
	req = httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/manifest", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with Bearer, got %d", rr.Code)
	}

	// 3. X-API-Key Header -> 200
	req = httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/manifest", nil)
	req.Header.Set("X-API-Key", apiKey)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with X-API-Key, got %d", rr.Code)
	}

	// 4. Query Parameter ?api_key= -> 200
	req = httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/manifest?api_key="+apiKey, nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with query param, got %d", rr.Code)
	}

	// Verify Audit Log sanitization: query param should be [REDACTED]
	logs, _, err := database.ListAuditLogs(context.Background(), 10, 0, "")
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	foundQueryLog := false
	for _, l := range logs {
		if bytes.Contains([]byte(l.Path), []byte("api_key=")) {
			foundQueryLog = true
			if bytes.Contains([]byte(l.Path), []byte(apiKey)) {
				t.Errorf("API key was NOT redacted from audit log: %s", l.Path)
			}
			if !bytes.Contains([]byte(l.Path), []byte("REDACTED")) {
				t.Errorf("expected REDACTED in path: %s", l.Path)
			}
		}
	}
	if !foundQueryLog {
		t.Error("expected audit log entry for query param request")
	}
}

func TestDownloadAndConditionalRequests(t *testing.T) {
	server, database, _, apiKey, _ := setupTestServer(t)
	defer database.Close()

	handler := server.Handler()

	// 1. Initial download -> 200 OK
	req := httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/download", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("download failed: %d, body: %s", rr.Code, rr.Body.String())
	}

	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Error("expected ETag header in response")
	}

	// 2. Conditional request with matching If-None-Match -> 304 Not Modified
	req = httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/download", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Errorf("expected 304 Not Modified, got %d", rr.Code)
	}

	// 3. HTTP Range Request -> 206 Partial Content
	req = httptest.NewRequest(http.MethodGet, "/v1/products/geolite-city/download", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Range", "bytes=0-5")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusPartialContent {
		t.Errorf("expected 206 Partial Content, got %d", rr.Code)
	}
	if rr.Body.String() != "binary" {
		t.Errorf("expected 'binary', got %q", rr.Body.String())
	}
}

func TestBreakGlassRecovery(t *testing.T) {
	server, database, _, _, recoverySecret := setupTestServer(t)
	defer database.Close()

	handler := server.Handler()

	// 1. Wrong recovery secret -> 401
	payload := map[string]string{
		"recovery_secret": "wrong-secret",
		"username":        "newadmin",
		"new_password":    "NewSuperSecurePassword123!",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong recovery secret, got %d", rr.Code)
	}

	// 2. Correct recovery secret -> 200 OK
	payload["recovery_secret"] = recoverySecret
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/recover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid recovery, got %d, body: %s", rr.Code, rr.Body.String())
	}

	// Verify new admin exists and password matches
	user, err := database.GetUserByUsername(context.Background(), "newadmin")
	if err != nil {
		t.Fatalf("failed to query recovered admin: %v", err)
	}
	match, err := auth.VerifyPassword("NewSuperSecurePassword123!", user.PasswordHash)
	if err != nil || !match {
		t.Error("recovered user password does not match")
	}
}
