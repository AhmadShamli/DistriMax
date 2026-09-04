package ui

import (
	"context"
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

	store, _ := storage.NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))
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

	// 4. Test Operations page with WebhookDeliveries
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
