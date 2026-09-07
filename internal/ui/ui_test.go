package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/config"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

func setupTestUI(t *testing.T) (*AdminUI, *http.ServeMux, *db.DB, *config.Config) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	database, err := db.Open(filepath.Join(tmpDir, "ui_test.sqlite3"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	_ = db.RunMigrations(ctx, database.DB)

	fsStore, _ := storage.NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))
	store := storage.NewStorageManager(fsStore)
	cfg := &config.Config{
		BootstrapSecret: "initial-bootstrap-token",
		StorageBackend:  "filesystem",
	}

	adminUI, err := NewAdminUI(database, cfg, store, nil)
	if err != nil {
		t.Fatalf("NewAdminUI failed: %v", err)
	}

	mux := http.NewServeMux()
	adminUI.RegisterRoutes(mux)

	return adminUI, mux, database, cfg
}

func TestSetupWizardFlow(t *testing.T) {
	_, mux, database, _ := setupTestUI(t)
	defer database.Close()

	// 1. GET /setup -> 200 OK
	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for GET /setup, got %d", rr.Code)
	}

	// 2. POST /setup with wrong bootstrap secret -> error
	form := url.Values{
		"bootstrap_secret": {"wrong-secret"},
		"username":         {"admin"},
		"password":         {"SuperSecret123!"},
		"confirm_password": {"SuperSecret123!"},
	}
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "Invalid bootstrap secret") {
		t.Errorf("expected invalid bootstrap secret message")
	}

	// 3. POST /setup with correct bootstrap secret -> 302 Redirect to /admin/dashboard
	form.Set("bootstrap_secret", "initial-bootstrap-token")
	req = httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect after setup, got %d", rr.Code)
	}

	cookie := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookie {
		if c.Name == "distrimax_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected distrimax_session cookie to be set")
	}

	// Verify setup_completed is true
	val, _, _ := database.GetSetting(context.Background(), "setup_completed")
	if val != "true" {
		t.Errorf("expected setup_completed to be true, got %s", val)
	}

	// 4. Accessing /admin/dashboard with the session cookie -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 on /admin/dashboard, got %d", rr.Code)
	}
}

func TestCSRFProtectionAndSessionAuth(t *testing.T) {
	_, mux, database, _ := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()

	// Seed completed setup and an admin user
	hash, _ := auth.HashPassword("AdminPass123!")
	u, _ := database.CreateUser(ctx, "sysadmin", hash)
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "test")

	// Create session
	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, u.ID, time.Now().UTC().Add(1*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// 1. Mutating POST without CSRF token -> 403 Forbidden
	form := url.Values{"display_name": {"Test Key"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/api-keys/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 without CSRF token, got %d", rr.Code)
	}

	// 2. Mutating POST with valid CSRF token -> 302 Redirect
	form.Set("csrf_token", sessID)
	form.Set("allowed_products", "*")
	form.Set("scopes", "manifest:read,download")
	req = httptest.NewRequest(http.MethodPost, "/admin/api-keys/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected 302 with valid CSRF, got %d", rr.Code)
	}
}

func TestAdminUserGuard(t *testing.T) {
	_, mux, database, _ := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()

	hash, _ := auth.HashPassword("AdminPass123!")
	u, _ := database.CreateUser(ctx, "onlyadmin", hash)
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "test")

	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, u.ID, time.Now().UTC().Add(1*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// Attempt to disable the only active admin
	form := url.Values{
		"csrf_token": {sessID},
		"user_id":    {u.ID},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/users/disable", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if !strings.Contains(rr.Header().Get("Location"), "Cannot+disable+the+last+active+administrator") {
		t.Errorf("expected last admin error in redirect Location, got %s", rr.Header().Get("Location"))
	}
}

func TestLiveTestHandlers(t *testing.T) {
	_, mux, database, cfg := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()

	cfg.SettingsEncryptionKey = []byte("12345678901234567890123456789012") // 32 bytes

	hash, _ := auth.HashPassword("AdminPass123!")
	u, _ := database.CreateUser(ctx, "sysadmin", hash)
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "test")

	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, u.ID, time.Now().UTC().Add(1*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// 1. Test Webhook with mock server
	receivedWebhook := false
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedWebhook = true
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookServer.Close()

	_ = database.SetSetting(ctx, "webhook_url", webhookServer.URL, false, "test")

	req := httptest.NewRequest(http.MethodPost, "/admin/settings/test-webhook", nil)
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Errorf("expected status ok in webhook test response, got %s", rr.Body.String())
	}
	if !receivedWebhook {
		t.Error("expected webhook server to receive ping")
	}

	// 2. Test S3 missing bucket
	req = httptest.NewRequest(http.MethodPost, "/admin/settings/test-s3", nil)
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "S3 bucket name is not configured") {
		t.Errorf("expected missing bucket error, got %s", rr.Body.String())
	}

	// 3. Test MaxMind missing credentials
	req = httptest.NewRequest(http.MethodPost, "/admin/settings/test-maxmind", nil)
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "MaxMind account ID or license key is not configured") {
		t.Errorf("expected missing credentials error, got %s", rr.Body.String())
	}

	// 4. Test Filesystem read/write test endpoint
	req = httptest.NewRequest(http.MethodPost, "/admin/settings/test-fs", nil)
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/settings/test-fs, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Errorf("expected status ok in test-fs response, got %s", rr.Body.String())
	}

	// 5. Test Settings page renders default cron and staleness values
	req = httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/settings, got %d", rr.Code)
	}
	settingsBody := rr.Body.String()
	if !strings.Contains(settingsBody, `value="0 4 * * *"`) {
		t.Errorf("expected default sync cron value '0 4 * * *' in settings, got %s", settingsBody)
	}
	if !strings.Contains(settingsBody, `value="8"`) {
		t.Errorf("expected default staleness days value '8' in settings, got %s", settingsBody)
	}
	if !strings.Contains(settingsBody, `autocomplete="new-password"`) {
		t.Errorf("expected new-password autocomplete in settings")
	}

	// 6. Test /admin/users/ with trailing slash and autofill prevention
	req = httptest.NewRequest(http.MethodGet, "/admin/users/", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/users/, got %d", rr.Code)
	}
	usersBody := rr.Body.String()
	if !strings.Contains(usersBody, `autocomplete="new-password"`) {
		t.Errorf("expected new-password autocomplete on user form")
	}

	// 7. Test Operations page with WebhookDeliveries
	req = httptest.NewRequest(http.MethodGet, "/admin/operations", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/operations, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Recent Webhook Deliveries") {
		t.Errorf("expected operations page to render Webhook Deliveries table")
	}
}

func TestStorageDriverSetupAndSwitching(t *testing.T) {
	adminUI, mux, database, cfg := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()

	cfg.SettingsEncryptionKey = []byte("12345678901234567890123456789012") // 32 bytes

	// 1. First-time setup with default storage driver (empty/filesystem)
	form := url.Values{
		"bootstrap_secret": {"initial-bootstrap-token"},
		"username":         {"admin"},
		"password":         {"SuperSecret123!"},
		"confirm_password": {"SuperSecret123!"},
		"storage_backend":  {"filesystem"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect after setup, got %d", rr.Code)
	}

	backend, _, _ := database.GetSetting(ctx, "storage_backend")
	if backend != "filesystem" {
		t.Errorf("expected storage_backend to be filesystem, got %s", backend)
	}
	if adminUI.storageManager.ActiveDriver() != "filesystem" {
		t.Errorf("expected storage manager driver to be filesystem, got %s", adminUI.storageManager.ActiveDriver())
	}

	// 2. Obtain session cookie for admin
	var sessionCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "distrimax_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected distrimax_session cookie")
	}

	// 3. Switch to S3 storage driver in Settings
	settingsForm := url.Values{
		"csrf_token":            {sessionCookie.Value},
		"storage_backend":       {"s3"},
		"s3_endpoint":           {"http://127.0.0.1:9000"},
		"s3_bucket":             {"test-bucket"},
		"s3_region":             {"us-east-1"},
		"s3_access_key_id":      {"minioadmin"},
		"s3_secret_access_key":  {"minioadmin"},
		"s3_force_path_style":   {"true"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/settings/save", strings.NewReader(settingsForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect after settings save, got %d", rr.Code)
	}

	backend, _, _ = database.GetSetting(ctx, "storage_backend")
	if backend != "s3" {
		t.Errorf("expected storage_backend to be s3, got %s", backend)
	}
	if adminUI.storageManager.ActiveDriver() != "s3" {
		t.Errorf("expected storage manager active driver to be s3, got %s", adminUI.storageManager.ActiveDriver())
	}

	// 4. Switch back to filesystem
	settingsForm.Set("storage_backend", "filesystem")
	req = httptest.NewRequest(http.MethodPost, "/admin/settings/save", strings.NewReader(settingsForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	backend, _, _ = database.GetSetting(ctx, "storage_backend")
	if backend != "filesystem" {
		t.Errorf("expected storage_backend to be filesystem, got %s", backend)
	}
	if adminUI.storageManager.ActiveDriver() != "filesystem" {
		t.Errorf("expected storage manager active driver to be filesystem, got %s", adminUI.storageManager.ActiveDriver())
	}
}

func TestProfileAndPasswordChange(t *testing.T) {
	_, mux, database, _ := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "test")

	// 1. Create admin user and session
	hash, _ := auth.HashPassword("InitialPass123!")
	user, err := database.CreateUser(ctx, "profile_admin", hash)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, user.ID, time.Now().Add(1*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// 2. View /admin/profile
	req := httptest.NewRequest(http.MethodGet, "/admin/profile", nil)
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/profile, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Operator Profile & Security") {
		t.Errorf("expected profile page title in response")
	}

	// 3. Password change: wrong current password
	form := url.Values{
		"csrf_token":       {sessID},
		"current_password": {"WrongPassword!"},
		"new_password":     {"BrandNewPass123!"},
		"confirm_password": {"BrandNewPass123!"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/profile/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "flash_error=Incorrect+current+password") {
		t.Errorf("expected incorrect current password error, got loc=%s", rr.Header().Get("Location"))
	}

	// 4. Password change: mismatched new passwords
	form.Set("current_password", "InitialPass123!")
	form.Set("confirm_password", "DifferentPass123!")
	req = httptest.NewRequest(http.MethodPost, "/admin/profile/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "flash_error=New+passwords+do+not+match") {
		t.Errorf("expected mismatched passwords error, got loc=%s", rr.Header().Get("Location"))
	}

	// 5. Password change: success
	form.Set("confirm_password", "BrandNewPass123!")
	req = httptest.NewRequest(http.MethodPost, "/admin/profile/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "flash_success=Password+updated+successfully") {
		t.Errorf("expected success redirect, got loc=%s", rr.Header().Get("Location"))
	}

	// 6. Verify password was updated in DB
	updatedUser, _ := database.GetUserByID(ctx, user.ID)
	ok, _ := auth.VerifyPassword("BrandNewPass123!", updatedUser.PasswordHash)
	if !ok {
		t.Errorf("expected new password to verify against stored hash")
	}
}

func TestProductManualDownloadAndTopBarHealth(t *testing.T) {
	adminUI, mux, database, _ := setupTestUI(t)
	defer database.Close()
	ctx := context.Background()
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "test")

	// 1. Create user and session
	hash, _ := auth.HashPassword("Pass12345!")
	user, _ := database.CreateUser(ctx, "download_tester", hash)
	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, user.ID, time.Now().Add(1*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// Before syncing products: top bar should show UNHEALTHY and 0 Active Products
	req := httptest.NewRequest(http.MethodGet, "/admin/products", nil)
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "UNHEALTHY") || !strings.Contains(rr.Body.String(), "0 Active Products") {
		t.Errorf("expected UNHEALTHY and 0 Active Products before sync, got %s", rr.Body.String())
	}

	// 2. Publish a version for geolite-city
	payload := []byte("fake-geolite2-city-database-bytes")
	storagePath, err := adminUI.storage.StoreArtifact(ctx, "geolite-city", "2026-09-04", "GeoLite2-City.mmdb", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("failed to store artifact: %v", err)
	}

	pv := &db.ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-04",
		ReleasedAt:     time.Now().UTC(),
		SHA256:         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		SizeBytes:      int64(len(payload)),
		StorageBackend: "filesystem",
		StoragePath:    storagePath,
	}
	if err := database.PublishNewVersion(ctx, pv, 30); err != nil {
		t.Fatalf("failed to publish version: %v", err)
	}

	// 3. Check top bar: should now show HEALTHY and 1 Active Product
	req = httptest.NewRequest(http.MethodGet, "/admin/products", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	productsHtml := rr.Body.String()
	if !strings.Contains(productsHtml, "HEALTHY") {
		t.Errorf("expected HEALTHY in top bar status when product is synced, got %s", productsHtml)
	}
	if !strings.Contains(productsHtml, "1 Active Product") {
		t.Errorf("expected '1 Active Product' in top bar, got %s", productsHtml)
	}
	if !strings.Contains(productsHtml, "⬇️ Download MMDB") {
		t.Errorf("expected '⬇️ Download MMDB' button in card header")
	}
	if !strings.Contains(productsHtml, "Copy Temporary Link") {
		t.Errorf("expected 'Copy Temporary Link' button in card header")
	}
	if !strings.Contains(productsHtml, "tempLinkModalBackdrop") {
		t.Errorf("expected modal backdrop in products page")
	}
	if !strings.Contains(productsHtml, "/admin/products/download?product_id=geolite-city") {
		t.Errorf("expected download URL in products table")
	}

	// 4. Test downloading via GET /admin/products/download
	req = httptest.NewRequest(http.MethodGet, "/admin/products/download?product_id=geolite-city", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for /admin/products/download, got %d", rr.Code)
	}
	if rr.Header().Get("Content-Disposition") != `attachment; filename="GeoLite2-City.mmdb"` {
		t.Errorf("expected attachment header, got %s", rr.Header().Get("Content-Disposition"))
	}
	if !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Errorf("downloaded content mismatch, got %s", rr.Body.String())
	}
}

func TestProductTemporaryLinkGenerationAndDirectDownload(t *testing.T) {
	ctx := context.Background()
	_, mux, database, _ := setupTestUI(t)
	defer database.Close()

	// 1. Log in admin user
	adminPass := "SuperSecret123!"
	adminHash, _ := auth.HashPassword(adminPass)
	adminUser, err := database.CreateUser(ctx, "admin", adminHash)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	_ = database.SetSetting(ctx, "setup_completed", "true", false, "admin")

	sessID, _ := auth.GenerateSessionToken()
	_ = database.CreateSession(ctx, sessID, adminUser.ID, time.Now().UTC().Add(24*time.Hour))
	sessionCookie := &http.Cookie{Name: "distrimax_session", Value: sessID}

	// Create a published version
	pv := &db.ProductVersion{
		ProductID:      "geolite-city",
		Version:        "2026-09-04",
		ReleasedAt:     time.Now().UTC(),
		SHA256:         "mock-sha-test",
		SizeBytes:      100,
		StorageBackend: "filesystem",
		StoragePath:    "artifacts/geolite-city/2026-09-04/GeoLite2-City.mmdb",
	}
	_ = database.PublishNewVersion(ctx, pv, 30)

	// 2. Request temporary link without CSRF -> 403 Forbidden
	form := url.Values{
		"product_id":     {"geolite-city"},
		"duration_hours": {"24"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/products/temporary-link", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 without CSRF token, got %d", rr.Code)
	}

	// 3. Request temporary link with valid CSRF header -> 200 OK
	req = httptest.NewRequest(http.MethodPost, "/admin/products/temporary-link", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for temporary-link, got %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
	tok, ok := resp["token"].(string)
	if !ok || !strings.HasPrefix(tok, "dmt_") {
		t.Errorf("expected dmt_ token, got %v", resp["token"])
	}
	if !strings.Contains(resp["download_url"].(string), tok) {
		t.Errorf("expected download_url to contain token, got %v", resp["download_url"])
	}
	if !strings.Contains(resp["download_url"].(string), "GeoLite2-City.mmdb") {
		t.Errorf("expected download_url to contain filename GeoLite2-City.mmdb, got %v", resp["download_url"])
	}
	if !strings.Contains(resp["curl_command"].(string), "curl -L -O") || !strings.Contains(resp["curl_command"].(string), "-J") {
		t.Errorf("expected curl_command to contain curl -L -O and -J, got %v", resp["curl_command"])
	}
	if !strings.Contains(resp["curl_command"].(string), "GeoLite2-City.mmdb") {
		t.Errorf("expected curl_command to contain filename GeoLite2-City.mmdb, got %v", resp["curl_command"])
	}

	// Verify token in DB
	dbTok, err := database.GetDownloadToken(ctx, tok)
	if err != nil {
		t.Fatalf("GetDownloadToken failed: %v", err)
	}
	if dbTok.ProductID != "geolite-city" {
		t.Errorf("expected product geolite-city, got %s", dbTok.ProductID)
	}

	// 4. Request temporary link for specific version
	formSpecific := url.Values{
		"product_id":     {"geolite-city"},
		"version":        {"2026-09-04"},
		"duration_hours": {"6"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/products/temporary-link", strings.NewReader(formSpecific.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for specific version, got %d", rr.Code)
	}

	// 5. Request for non-existent product -> 404
	formBad := url.Values{
		"product_id": {"non-existent-product"},
	}
	req = httptest.NewRequest(http.MethodPost, "/admin/products/temporary-link", strings.NewReader(formBad.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", sessID)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bad product, got %d", rr.Code)
	}

	// 6. Test that accessing /admin/products/download with token redirects to /v1/products/...
	req = httptest.NewRequest(http.MethodGet, "/admin/products/download?product_id=geolite-city&token="+tok, nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("expected 302 redirect for /admin/products/download with token, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/v1/products/geolite-city/download") || !strings.Contains(loc, tok) || !strings.Contains(loc, "GeoLite2-City.mmdb") {
		t.Errorf("unexpected redirect location: %s", loc)
	}
}


