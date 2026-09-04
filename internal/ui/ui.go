package ui

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/config"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
	"github.com/AhmadShamli/DistriMax/internal/syncer"
	"github.com/AhmadShamli/DistriMax/internal/workers"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type AdminUI struct {
	db              *db.DB
	cfg             *config.Config
	storage         storage.StorageBackend
	storageManager  *storage.StorageManager
	syncer          *syncer.Syncer
	parsedTemplates map[string]*template.Template
}

func NewAdminUI(database *db.DB, cfg *config.Config, store storage.StorageBackend, syncEngine *syncer.Syncer) (*AdminUI, error) {
	var sm *storage.StorageManager
	if m, ok := store.(*storage.StorageManager); ok {
		sm = m
	}

	ui := &AdminUI{
		db:              database,
		cfg:             cfg,
		storage:         store,
		storageManager:  sm,
		syncer:          syncEngine,
		parsedTemplates: make(map[string]*template.Template),
	}

	pages := []string{"dashboard", "products", "downloads", "apikeys", "operations", "audit", "users", "settings", "profile"}
	for _, page := range pages {
		tmpl, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, fmt.Errorf("failed to parse template %s: %w", page, err)
		}
		ui.parsedTemplates[page] = tmpl
	}

	// Standalone auth templates
	setupTmpl, err := template.ParseFS(templateFS, "templates/setup.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse setup template: %w", err)
	}
	ui.parsedTemplates["setup"] = setupTmpl

	loginTmpl, err := template.ParseFS(templateFS, "templates/login.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse login template: %w", err)
	}
	ui.parsedTemplates["login"] = loginTmpl

	return ui, nil
}

// RegisterRoutes registers all UI handlers on the HTTP ServeMux.
func (u *AdminUI) RegisterRoutes(mux *http.ServeMux) {
	// Static Assets
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("GET /static/", fileServer)

	// Setup Wizard
	mux.HandleFunc("GET /setup", u.handleSetupGet)
	mux.HandleFunc("POST /setup", u.handleSetupPost)

	// Auth (Login / Logout)
	mux.HandleFunc("GET /login", u.handleLoginGet)
	mux.HandleFunc("POST /login", u.handleLoginPost)
	mux.HandleFunc("POST /admin/logout", u.handleLogout)

	// Admin Pages (Protected by Session Auth)
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/dashboard", u.requireAuth(u.handleDashboard))
	mux.HandleFunc("GET /admin/products", u.requireAuth(u.handleProducts))
	mux.HandleFunc("GET /admin/products/download", u.requireAuth(u.handleProductDownload))
	mux.HandleFunc("POST /admin/products/sync", u.requireAuth(u.handleProductSync))
	mux.HandleFunc("POST /admin/products/rollback", u.requireAuth(u.handleProductRollback))

	mux.HandleFunc("GET /admin/api-keys", u.requireAuth(u.handleAPIKeys))
	mux.HandleFunc("POST /admin/api-keys/create", u.requireAuth(u.handleAPIKeyCreate))
	mux.HandleFunc("POST /admin/api-keys/revoke", u.requireAuth(u.handleAPIKeyRevoke))

	mux.HandleFunc("GET /admin/downloads", u.requireAuth(u.handleDownloads))
	mux.HandleFunc("GET /admin/audit", u.requireAuth(u.handleAudit))

	mux.HandleFunc("GET /admin/operations", u.requireAuth(u.handleOperations))
	mux.HandleFunc("POST /admin/operations/sync-all", u.requireAuth(u.handleSyncAll))
	mux.HandleFunc("POST /admin/operations/cleanup", u.requireAuth(u.handleRunCleanup))
	mux.HandleFunc("POST /admin/operations/backup", u.requireAuth(u.handleCreateBackup))

	mux.HandleFunc("GET /admin/users", u.requireAuth(u.handleUsers))
	mux.HandleFunc("GET /admin/users/", u.requireAuth(u.handleUsers))
	mux.HandleFunc("POST /admin/users/create", u.requireAuth(u.handleUserCreate))
	mux.HandleFunc("POST /admin/users/disable", u.requireAuth(u.handleUserDisable))
	mux.HandleFunc("POST /admin/users/enable", u.requireAuth(u.handleUserEnable))

	mux.HandleFunc("GET /admin/profile", u.requireAuth(u.handleProfile))
	mux.HandleFunc("POST /admin/profile/change-password", u.requireAuth(u.handleProfileChangePassword))

	mux.HandleFunc("GET /admin/settings", u.requireAuth(u.handleSettings))
	mux.HandleFunc("POST /admin/settings/save", u.requireAuth(u.handleSettingsSave))
	mux.HandleFunc("POST /admin/settings/test-maxmind", u.requireAuth(u.handleTestMaxMind))
	mux.HandleFunc("POST /admin/settings/test-s3", u.requireAuth(u.handleTestS3))
	mux.HandleFunc("POST /admin/settings/test-fs", u.requireAuth(u.handleTestFilesystem))
	mux.HandleFunc("POST /admin/settings/test-webhook", u.requireAuth(u.handleTestWebhook))
}

// ReloadStorage synchronizes the active storage driver in StorageManager with the database settings.
func (u *AdminUI) ReloadStorage(ctx context.Context) error {
	if u.storageManager == nil {
		return nil
	}

	backend, _, _ := u.db.GetSetting(ctx, "storage_backend")
	if backend != "s3" {
		backend = "filesystem"
	}

	if backend == "s3" {
		endpoint, _, _ := u.db.GetSetting(ctx, "s3_endpoint")
		bucket, _, _ := u.db.GetSetting(ctx, "s3_bucket")
		region, _, _ := u.db.GetSetting(ctx, "s3_region")
		ak, _, _ := u.db.GetSetting(ctx, "s3_access_key_id")
		encSK, isEnc, _ := u.db.GetSetting(ctx, "s3_secret_access_key")
		forcePath, _, _ := u.db.GetSetting(ctx, "s3_force_path_style")

		sk := encSK
		if isEnc && len(u.cfg.SettingsEncryptionKey) == 32 && encSK != "" {
			if dec, err := auth.Decrypt(encSK, u.cfg.SettingsEncryptionKey); err == nil {
				sk = string(dec)
			}
		}

		if bucket != "" {
			s3Store, err := storage.NewS3Storage(storage.S3Config{
				Endpoint:        endpoint,
				Bucket:          bucket,
				Region:          region,
				AccessKeyID:     ak,
				SecretAccessKey: sk,
				ForcePathStyle:  forcePath == "true",
			})
			if err == nil {
				u.storageManager.SetS3Storage(s3Store)
				_ = u.storageManager.SetActiveDriver("s3")
				return nil
			}
		}
	}

	// Default fallback to filesystem
	_ = u.storageManager.SetActiveDriver("filesystem")
	return nil
}

// Session & CSRF Middleware

type adminContextKey string

const (
	adminUserKey  adminContextKey = "adminUser"
	adminSessKey  adminContextKey = "adminSession"
	adminCSRFKey  adminContextKey = "adminCSRF"
)

func (u *AdminUI) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check setup state
		setupDone, _, _ := u.db.GetSetting(r.Context(), "setup_completed")
		if setupDone != "true" {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}

		cookie, err := r.Cookie("distrimax_session")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		sess, user, err := u.db.GetSession(r.Context(), cookie.Value)
		if err != nil || user == nil || user.IsDisabled {
			http.SetCookie(w, &http.Cookie{Name: "distrimax_session", Value: "", MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// Mutating methods require CSRF validation
		if r.Method == http.MethodPost {
			token := r.FormValue("csrf_token")
			if token == "" {
				token = r.Header.Get("X-CSRF-Token")
			}
			if !auth.ValidateCSRFToken(cookie.Value, token) {
				http.Error(w, "CSRF token mismatch", http.StatusForbidden)
				return
			}
		}

		ctx := context.WithValue(r.Context(), adminUserKey, user)
		ctx = context.WithValue(ctx, adminSessKey, sess)
		ctx = context.WithValue(ctx, adminCSRFKey, cookie.Value) // Session token acts as synchronizer token

		next(w, r.WithContext(ctx))
	}
}

// Handler implementations

func (u *AdminUI) handleSetupGet(w http.ResponseWriter, r *http.Request) {
	setupDone, _, _ := u.db.GetSetting(r.Context(), "setup_completed")
	if setupDone == "true" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	_ = u.parsedTemplates["setup"].Execute(w, nil)
}

func (u *AdminUI) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	setupDone, _, _ := u.db.GetSetting(r.Context(), "setup_completed")
	if setupDone == "true" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	secret := r.FormValue("bootstrap_secret")
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if subtle.ConstantTimeCompare([]byte(secret), []byte(u.cfg.BootstrapSecret)) != 1 {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "Invalid bootstrap secret"})
		return
	}

	if username == "" || len(password) < 8 || password != confirm {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "Passwords must match and be at least 8 characters"})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "Failed to hash password"})
		return
	}

	storageBackend := strings.ToLower(strings.TrimSpace(r.FormValue("storage_backend")))
	if storageBackend != "s3" {
		storageBackend = "filesystem"
	}

	s3Endpoint := strings.TrimSpace(r.FormValue("s3_endpoint"))
	s3Bucket := strings.TrimSpace(r.FormValue("s3_bucket"))
	s3Region := strings.TrimSpace(r.FormValue("s3_region"))
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	s3AccessKey := strings.TrimSpace(r.FormValue("s3_access_key_id"))
	s3SecretKey := strings.TrimSpace(r.FormValue("s3_secret_access_key"))
	s3ForcePath := "false"
	if r.FormValue("s3_force_path_style") == "true" {
		s3ForcePath = "true"
	}

	if storageBackend == "s3" && s3Bucket == "" {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "S3 bucket name is required when selecting S3 storage driver"})
		return
	}

	err = u.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO users (id, username, password_hash, is_disabled, created_at, updated_at) VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", "admin-1", username, hash)
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE settings SET value = 'true', updated_at = CURRENT_TIMESTAMP WHERE key = 'setup_completed'")
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE settings SET value = ?, updated_at = CURRENT_TIMESTAMP WHERE key = 'storage_backend'", storageBackend)
		if err != nil {
			return err
		}

		if storageBackend == "s3" {
			_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_endpoint', ?, 0, 'setup', CURRENT_TIMESTAMP)", s3Endpoint)
			_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_bucket', ?, 0, 'setup', CURRENT_TIMESTAMP)", s3Bucket)
			_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_region', ?, 0, 'setup', CURRENT_TIMESTAMP)", s3Region)
			_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_access_key_id', ?, 0, 'setup', CURRENT_TIMESTAMP)", s3AccessKey)
			_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_force_path_style', ?, 0, 'setup', CURRENT_TIMESTAMP)", s3ForcePath)

			if s3SecretKey != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
				enc, err := auth.Encrypt([]byte(s3SecretKey), u.cfg.SettingsEncryptionKey)
				if err == nil {
					_, _ = tx.Exec("INSERT OR REPLACE INTO settings (key, value, is_encrypted, updated_by, updated_at) VALUES ('s3_secret_access_key', ?, 1, 'setup', CURRENT_TIMESTAMP)", enc)
				}
			}
		}
		return nil
	})
	if err != nil {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "Failed to initialize database: " + err.Error()})
		return
	}

	_ = u.ReloadStorage(r.Context())

	// Create session and log in immediately
	sessID, _ := auth.GenerateSessionToken()
	_ = u.db.CreateSession(r.Context(), sessID, "admin-1", time.Now().UTC().Add(24*time.Hour))

	http.SetCookie(w, &http.Cookie{
		Name:     "distrimax_session",
		Value:    sessID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/admin/dashboard?flash_success=DistriMax+successfully+initialized!", http.StatusFound)
}

func (u *AdminUI) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	setupDone, _, _ := u.db.GetSetting(r.Context(), "setup_completed")
	if setupDone != "true" {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	_ = u.parsedTemplates["login"].Execute(w, nil)
}

func (u *AdminUI) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := u.db.GetUserByUsername(r.Context(), username)
	if err != nil || user == nil || user.IsDisabled {
		_ = u.parsedTemplates["login"].Execute(w, map[string]string{"Error": "Invalid username or password"})
		return
	}

	match, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !match {
		_ = u.parsedTemplates["login"].Execute(w, map[string]string{"Error": "Invalid username or password"})
		return
	}

	sessID, _ := auth.GenerateSessionToken()
	_ = u.db.CreateSession(r.Context(), sessID, user.ID, time.Now().UTC().Add(24*time.Hour))

	http.SetCookie(w, &http.Cookie{
		Name:     "distrimax_session",
		Value:    sessID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
}

func (u *AdminUI) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("distrimax_session"); err == nil {
		_ = u.db.RevokeSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "distrimax_session", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// View Data Helpers

type BaseViewData struct {
	Title              string
	ActiveNav          string
	HealthStatus       string
	ActiveProductCount int
	CurrentUser        *db.User
	CSRFToken          string
	FlashSuccess       string
	FlashError         string
	MaxMindConfigured  bool
}

func (u *AdminUI) isMaxMindConfigured(ctx context.Context) bool {
	accountID, _, _ := u.db.GetSetting(ctx, "maxmind_account_id")
	encLicenseKey, _, _ := u.db.GetSetting(ctx, "maxmind_license_key")
	return strings.TrimSpace(accountID) != "" && strings.TrimSpace(encLicenseKey) != ""
}

func (u *AdminUI) buildBaseData(r *http.Request, title, nav string) BaseViewData {
	user, _ := r.Context().Value(adminUserKey).(*db.User)
	csrf, _ := r.Context().Value(adminCSRFKey).(string)

	products, _ := u.db.ListProducts(r.Context())
	syncedCount := 0
	staleCount := 0
	now := time.Now().UTC()

	stalenessDays := 8
	if val, _, err := u.db.GetSetting(r.Context(), "staleness_threshold_days"); err == nil && val != "" {
		if d, err := strconv.Atoi(val); err == nil && d > 0 {
			stalenessDays = d
		}
	}

	for _, p := range products {
		if p.IsEnabled {
			cur, err := u.db.GetCurrentVersion(r.Context(), p.ID)
			if err == nil && cur != nil && !cur.IsDeleted {
				syncedCount++
				threshold := p.StalenessDays
				if threshold <= 0 {
					threshold = stalenessDays
				}
				if now.Sub(cur.ReleasedAt) > time.Duration(threshold)*24*time.Hour {
					staleCount++
				}
			}
		}
	}

	health := "HEALTHY"
	if syncedCount == 0 {
		health = "UNHEALTHY"
	} else if staleCount > 0 {
		health = "DEGRADED"
	}

	return BaseViewData{
		Title:              title,
		ActiveNav:          nav,
		HealthStatus:       health,
		ActiveProductCount: syncedCount,
		CurrentUser:        user,
		CSRFToken:          csrf,
		FlashSuccess:       r.URL.Query().Get("flash_success"),
		FlashError:         r.URL.Query().Get("flash_error"),
		MaxMindConfigured:  u.isMaxMindConfigured(r.Context()),
	}
}

func (u *AdminUI) newViewData(base BaseViewData, extra map[string]interface{}) map[string]interface{} {
	data := map[string]interface{}{
		"BaseViewData":       base,
		"Title":              base.Title,
		"ActiveNav":          base.ActiveNav,
		"HealthStatus":       base.HealthStatus,
		"ActiveProductCount": base.ActiveProductCount,
		"CurrentUser":        base.CurrentUser,
		"CSRFToken":          base.CSRFToken,
		"FlashSuccess":       base.FlashSuccess,
		"FlashError":         base.FlashError,
		"MaxMindConfigured":  base.MaxMindConfigured,
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

func (u *AdminUI) handleDashboard(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Dashboard", "dashboard")

	products, _ := u.db.ListProducts(r.Context())
	type prodView struct {
		ID             string
		DisplayName    string
		CurrentVersion *db.ProductVersion
	}
	var pList []prodView
	for _, p := range products {
		cur, _ := u.db.GetCurrentVersion(r.Context(), p.ID)
		pList = append(pList, prodView{
			ID:             p.ID,
			DisplayName:    p.DisplayName,
			CurrentVersion: cur,
		})
	}

	stats, _ := u.db.GetDownloadStats(r.Context(), 24)
	if stats == nil {
		stats = &db.DownloadStats{}
	}

	storageBackend, _, _ := u.db.GetSetting(r.Context(), "storage_backend")
	if storageBackend == "" {
		storageBackend = "filesystem"
	}

	// Server-side SVG sparkline points (generated from simple trend)
	svgPoints := "0,50 30,45 60,35 90,40 120,20 150,25 180,15 210,30 240,10 270,18 300,5"

	data := u.newViewData(base, map[string]interface{}{
		"Products":       pList,
		"Stats":          stats,
		"StorageBackend": strings.ToUpper(storageBackend),
		"SVGPoints":      svgPoints,
	})

	_ = u.parsedTemplates["dashboard"].Execute(w, data)
}

func (u *AdminUI) handleProducts(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Products", "products")
	products, _ := u.db.ListProducts(r.Context())

	type productWithVersions struct {
		Product        *db.Product
		CurrentVersion *db.ProductVersion
		Versions       []*db.ProductVersion
	}
	var list []productWithVersions
	for _, p := range products {
		versions, _ := u.db.ListProductVersions(r.Context(), p.ID)
		cur, _ := u.db.GetCurrentVersion(r.Context(), p.ID)
		list = append(list, productWithVersions{
			Product:        p,
			CurrentVersion: cur,
			Versions:       versions,
		})
	}

	data := u.newViewData(base, map[string]interface{}{
		"Products": list,
	})
	_ = u.parsedTemplates["products"].Execute(w, data)
}

func (u *AdminUI) handleProductSync(w http.ResponseWriter, r *http.Request) {
	redirectTarget := "/admin/products"
	if strings.Contains(r.Header.Get("Referer"), "/admin/dashboard") {
		redirectTarget = "/admin/dashboard"
	}

	if !u.isMaxMindConfigured(r.Context()) {
		http.Redirect(w, r, fmt.Sprintf("%s?flash_error=Cannot+sync:+MaxMind+Account+ID+and+License+Key+are+not+configured.+Please+configure+them+in+Settings.", redirectTarget), http.StatusFound)
		return
	}

	productID := r.FormValue("product_id")
	res, err := u.syncer.SyncProduct(r.Context(), productID, "MANUAL")
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("%s?flash_error=Sync+failed:+%s", redirectTarget, url.QueryEscape(err.Error())), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s?flash_success=Sync+completed:+%s+(%s)", redirectTarget, url.QueryEscape(res.ProductID), url.QueryEscape(res.Status)), http.StatusFound)
}

func (u *AdminUI) handleProductRollback(w http.ResponseWriter, r *http.Request) {
	productID := r.FormValue("product_id")
	version := r.FormValue("version")
	err := u.db.RollbackToVersion(r.Context(), productID, version, 30)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_error=Rollback+failed:+%s", err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_success=Successfully+rolled+back+%s+to+version+%s", productID, version), http.StatusFound)
}

func (u *AdminUI) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "API Keys", "apikeys")
	keys, _ := u.db.ListAPIKeys(r.Context())

	data := u.newViewData(base, map[string]interface{}{
		"Keys":         keys,
		"NewKeySecret": r.URL.Query().Get("new_key_secret"),
	})
	_ = u.parsedTemplates["apikeys"].Execute(w, data)
}

func (u *AdminUI) handleAPIKeyCreate(w http.ResponseWriter, r *http.Request) {
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	allowed := r.FormValue("allowed_products")
	scopes := r.FormValue("scopes")
	rateLimit, _ := strconv.Atoi(r.FormValue("rate_limit_per_min"))
	if rateLimit <= 0 {
		rateLimit = 60
	}

	fullSecret, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		http.Redirect(w, r, "/admin/api-keys?flash_error=Failed+to+generate+key", http.StatusFound)
		return
	}

	key := &db.APIKey{
		KeyPrefix:       prefix,
		KeyHash:         hash,
		DisplayName:     displayName,
		Scopes:          scopes,
		AllowedProducts: allowed,
		RateLimitPerMin: rateLimit,
	}

	if err := u.db.CreateAPIKey(r.Context(), key); err != nil {
		http.Redirect(w, r, "/admin/api-keys?flash_error=Failed+to+store+key", http.StatusFound)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/api-keys?new_key_secret=%s&flash_success=API+key+created+successfully", fullSecret), http.StatusFound)
}

func (u *AdminUI) handleAPIKeyRevoke(w http.ResponseWriter, r *http.Request) {
	keyID := r.FormValue("key_id")
	_ = u.db.RevokeAPIKey(r.Context(), keyID)
	http.Redirect(w, r, "/admin/api-keys?flash_success=API+key+revoked", http.StatusFound)
}

func (u *AdminUI) handleDownloads(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Downloads", "downloads")
	stats, _ := u.db.GetDownloadStats(r.Context(), 24)
	logs, _, _ := u.db.ListAuditLogs(r.Context(), 20, 0, "")

	data := u.newViewData(base, map[string]interface{}{
		"Stats":           stats,
		"RecentDownloads": logs,
	})
	_ = u.parsedTemplates["downloads"].Execute(w, data)
}

func (u *AdminUI) handleAudit(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Audit Logs", "audit")

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 50
	offset := (page - 1) * limit
	product := r.URL.Query().Get("product")

	logs, total, _ := u.db.ListAuditLogs(r.Context(), limit, offset, product)

	data := u.newViewData(base, map[string]interface{}{
		"Logs":            logs,
		"CurrentPage":     page,
		"PrevPage":        page - 1,
		"NextPage":        page + 1,
		"HasNextPage":     (page * limit) < total,
		"TotalCount":      total,
		"SelectedProduct": product,
	})
	_ = u.parsedTemplates["audit"].Execute(w, data)
}

func (u *AdminUI) handleOperations(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Operations", "operations")

	// Query recent sync runs
	rows, err := u.db.QueryContext(r.Context(), "SELECT id, product_id, status, trigger_type, version_discovered, duration_ms, error_message, created_at FROM sync_runs ORDER BY created_at DESC LIMIT 10")
	type syncView struct {
		ID                string
		ProductID         string
		Status            string
		TriggerType       string
		VersionDiscovered string
		DurationMs        int
		ErrorMessage      string
		CreatedAt         time.Time
	}
	var runs []syncView
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var s syncView
			var vDisc, errMsg sql.NullString
			if err := rows.Scan(&s.ID, &s.ProductID, &s.Status, &s.TriggerType, &vDisc, &s.DurationMs, &errMsg, &s.CreatedAt); err == nil {
				s.VersionDiscovered = vDisc.String
				s.ErrorMessage = errMsg.String
				runs = append(runs, s)
			}
		}
	}

	// Query recent webhook deliveries
	whRows, err := u.db.QueryContext(r.Context(), "SELECT id, event_type, product_id, version, target_url, status_code, attempt_count, duration_ms, error_message, created_at FROM webhook_deliveries ORDER BY created_at DESC LIMIT 10")
	type webhookView struct {
		ID           string
		EventType    string
		ProductID    string
		Version      string
		TargetURL    string
		StatusCode   int
		AttemptCount int
		DurationMs   int
		ErrorMessage string
		CreatedAt    time.Time
	}
	var deliveries []webhookView
	if err == nil {
		defer whRows.Close()
		for whRows.Next() {
			var d webhookView
			var pID, ver, errMsg sql.NullString
			var statusCode, duration sql.NullInt64
			if err := whRows.Scan(&d.ID, &d.EventType, &pID, &ver, &d.TargetURL, &statusCode, &d.AttemptCount, &duration, &errMsg, &d.CreatedAt); err == nil {
				d.ProductID = pID.String
				d.Version = ver.String
				d.StatusCode = int(statusCode.Int64)
				d.DurationMs = int(duration.Int64)
				d.ErrorMessage = errMsg.String
				deliveries = append(deliveries, d)
			}
		}
	}

	data := u.newViewData(base, map[string]interface{}{
		"SyncRuns":          runs,
		"WebhookDeliveries": deliveries,
	})
	_ = u.parsedTemplates["operations"].Execute(w, data)
}

func (u *AdminUI) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	if !u.isMaxMindConfigured(r.Context()) {
		http.Redirect(w, r, "/admin/operations?flash_error=Cannot+sync:+MaxMind+Account+ID+and+License+Key+are+not+configured.+Please+configure+them+in+Settings.", http.StatusFound)
		return
	}

	products, _ := u.db.ListProducts(r.Context())
	var failed []string
	var successCount int
	for _, p := range products {
		if p.IsEnabled {
			res, err := u.syncer.SyncProduct(r.Context(), p.ID, "MANUAL")
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s (%s)", p.DisplayName, err.Error()))
			} else if res != nil {
				successCount++
			}
		}
	}
	if len(failed) > 0 {
		http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_error=Sync+failed+for:+%s", url.QueryEscape(strings.Join(failed, "; "))), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_success=Upstream+sync+completed+successfully+for+%d+enabled+product(s)", successCount), http.StatusFound)
}

func (u *AdminUI) handleRunCleanup(w http.ResponseWriter, r *http.Request) {
	res, err := workers.RunCleanup(r.Context(), u.db, u.storage)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_error=Cleanup+failed:+%s", err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_success=Cleanup+completed:+%d+artifacts+and+%d+audit+logs+purged", res.PurgedVersions, res.PurgedAuditLogs), http.StatusFound)
}

func (u *AdminUI) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	path, err := workers.CreateBackup(r.Context(), u.db, u.cfg.BackupRoot, nil, 7)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_error=Backup+failed:+%s", err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/operations?flash_success=Backup+snapshot+created:+%s", filepath.Base(path)), http.StatusFound)
}

func (u *AdminUI) handleUsers(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Users", "users")
	users, _ := u.db.ListUsers(r.Context())

	data := u.newViewData(base, map[string]interface{}{
		"Users": users,
	})
	_ = u.parsedTemplates["users"].Execute(w, data)
}

func (u *AdminUI) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || len(password) < 8 {
		http.Redirect(w, r, "/admin/users?flash_error=Username+and+password+(min+8+chars)+are+required", http.StatusFound)
		return
	}

	hash, _ := auth.HashPassword(password)
	_, err := u.db.CreateUser(r.Context(), username, hash)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/users?flash_error=Failed+to+create+user:+%s", err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/users?flash_success=Admin+user+%s+created", username), http.StatusFound)
}

func (u *AdminUI) handleUserDisable(w http.ResponseWriter, r *http.Request) {
	userID := r.FormValue("user_id")
	err := u.db.SetUserDisabled(r.Context(), userID, true)
	if err != nil {
		if errors.Is(err, db.ErrCannotDisableLastAdmin) {
			http.Redirect(w, r, "/admin/users?flash_error=Cannot+disable+the+last+active+administrator", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/admin/users?flash_error="+err.Error(), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/users?flash_success=User+disabled", http.StatusFound)
}

func (u *AdminUI) handleUserEnable(w http.ResponseWriter, r *http.Request) {
	userID := r.FormValue("user_id")
	_ = u.db.SetUserDisabled(r.Context(), userID, false)
	http.Redirect(w, r, "/admin/users?flash_success=User+enabled", http.StatusFound)
}

func (u *AdminUI) handleSettings(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Settings", "settings")

	settingsList, _ := u.db.ListSettings(r.Context())
	settingsMap := make(map[string]string)
	for _, s := range settingsList {
		settingsMap[s.Key] = s.Value
	}

	encLicenseKey, _, _ := u.db.GetSetting(r.Context(), "maxmind_license_key")

	// Ensure default values are populated so inputs are never empty
	if settingsMap["sync_schedule_cron"] == "" {
		settingsMap["sync_schedule_cron"] = "0 4 * * *"
	}
	if settingsMap["staleness_threshold_days"] == "" {
		settingsMap["staleness_threshold_days"] = "8"
	}
	if settingsMap["artifact_retention_days"] == "" {
		settingsMap["artifact_retention_days"] = "30"
	}
	if settingsMap["audit_retention_days"] == "" {
		settingsMap["audit_retention_days"] = "90"
	}
	if settingsMap["storage_backend"] == "" {
		settingsMap["storage_backend"] = "filesystem"
	}

	artifactRoot := "/var/lib/distrimax/artifacts"
	stagingRoot := "/var/lib/distrimax/staging"
	if u.cfg != nil {
		if u.cfg.ArtifactRoot != "" {
			artifactRoot = u.cfg.ArtifactRoot
		}
		if u.cfg.StagingRoot != "" {
			stagingRoot = u.cfg.StagingRoot
		}
	}

	data := u.newViewData(base, map[string]interface{}{
		"Settings":      settingsMap,
		"HasLicenseKey": strings.TrimSpace(encLicenseKey) != "",
		"ArtifactRoot":  artifactRoot,
		"StagingRoot":   stagingRoot,
	})
	_ = u.parsedTemplates["settings"].Execute(w, data)
}

func (u *AdminUI) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(adminUserKey).(*db.User)
	updatedBy := "admin"
	if user != nil {
		updatedBy = user.Username
	}

	keys := []string{
		"maxmind_account_id", "s3_endpoint", "s3_bucket",
		"s3_region", "s3_access_key_id", "sync_schedule_cron",
		"staleness_threshold_days", "artifact_retention_days", "audit_retention_days",
		"webhook_url",
	}

	for _, k := range keys {
		val := strings.TrimSpace(r.FormValue(k))
		if val != "" {
			_ = u.db.SetSetting(r.Context(), k, val, false, updatedBy)
		}
	}

	// Synchronize products defaults with updated schedule and retention
	cronVal := strings.TrimSpace(r.FormValue("sync_schedule_cron"))
	stalenessVal, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("staleness_threshold_days")))
	retentionVal, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("artifact_retention_days")))
	_ = u.db.UpdateProductsDefaults(r.Context(), cronVal, stalenessVal, retentionVal)

	// Storage Driver (defaults to filesystem)
	storageBackend := strings.ToLower(strings.TrimSpace(r.FormValue("storage_backend")))
	if storageBackend != "s3" {
		storageBackend = "filesystem"
	}
	_ = u.db.SetSetting(r.Context(), "storage_backend", storageBackend, false, updatedBy)

	// S3 Force Path Style
	forcePath := "false"
	if r.FormValue("s3_force_path_style") == "true" {
		forcePath = "true"
	}
	_ = u.db.SetSetting(r.Context(), "s3_force_path_style", forcePath, false, updatedBy)

	// Encrypted Secrets
	if secret := strings.TrimSpace(r.FormValue("maxmind_license_key")); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "maxmind_license_key", enc, true, updatedBy)
	}

	if secret := strings.TrimSpace(r.FormValue("s3_secret_access_key")); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "s3_secret_access_key", enc, true, updatedBy)
	}

	if secret := strings.TrimSpace(r.FormValue("webhook_hmac_secret")); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "webhook_hmac_secret", enc, true, updatedBy)
	}

	// Hot-reload storage driver
	_ = u.ReloadStorage(r.Context())

	http.Redirect(w, r, "/admin/settings?flash_success=Settings+saved+successfully", http.StatusFound)
}

func (u *AdminUI) handleTestMaxMind(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	accountID, _, _ := u.db.GetSetting(r.Context(), "maxmind_account_id")
	encLicenseKey, isEnc, _ := u.db.GetSetting(r.Context(), "maxmind_license_key")
	if accountID == "" || encLicenseKey == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "MaxMind account ID or license key is not configured"})
		return
	}

	if u.syncer == nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "Syncer engine not initialized"})
		return
	}

	licenseKey := encLicenseKey
	if isEnc && len(u.cfg.SettingsEncryptionKey) == 32 {
		dec, err := auth.Decrypt(encLicenseKey, u.cfg.SettingsEncryptionKey)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "Failed to decrypt license key"})
			return
		}
		licenseKey = string(dec)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := u.syncer.CheckCredentials(ctx, accountID, licenseKey); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (u *AdminUI) handleTestS3(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	endpoint, _, _ := u.db.GetSetting(r.Context(), "s3_endpoint")
	bucket, _, _ := u.db.GetSetting(r.Context(), "s3_bucket")
	region, _, _ := u.db.GetSetting(r.Context(), "s3_region")
	ak, _, _ := u.db.GetSetting(r.Context(), "s3_access_key_id")
	encSK, isEnc, _ := u.db.GetSetting(r.Context(), "s3_secret_access_key")
	forcePath, _, _ := u.db.GetSetting(r.Context(), "s3_force_path_style")

	if bucket == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "S3 bucket name is not configured"})
		return
	}

	sk := encSK
	if isEnc && len(u.cfg.SettingsEncryptionKey) == 32 && encSK != "" {
		if dec, err := auth.Decrypt(encSK, u.cfg.SettingsEncryptionKey); err == nil {
			sk = string(dec)
		}
	}

	s3Store, err := storage.NewS3Storage(storage.S3Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Region:          region,
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		ForcePathStyle:  forcePath == "true",
	})
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": fmt.Sprintf("Invalid S3 configuration: %v", err)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := s3Store.CheckBucket(ctx); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (u *AdminUI) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	webhookURL, _, _ := u.db.GetSetting(r.Context(), "webhook_url")
	if webhookURL == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "Webhook target URL is not configured"})
		return
	}

	encSecret, isEnc, _ := u.db.GetSetting(r.Context(), "webhook_hmac_secret")
	hmacSecret := encSecret
	if isEnc && len(u.cfg.SettingsEncryptionKey) == 32 && encSecret != "" {
		if dec, err := auth.Decrypt(encSecret, u.cfg.SettingsEncryptionKey); err == nil {
			hmacSecret = string(dec)
		}
	}

	event := &workers.WebhookEvent{
		Event:        "system.test_ping",
		Product:      "test",
		Version:      "ping",
		ReleasedAt:   time.Now().UTC(),
		SHA256:       "0000000000000000000000000000000000000000000000000000000000000000",
		SizeBytes:    0,
		DownloadPath: "/v1/products/test/manifest",
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := workers.SendWebhook(ctx, u.db, webhookURL, hmacSecret, event); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "status_code": 200})
}

func (u *AdminUI) handleTestFilesystem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if u.storageManager != nil {
		if err := u.storageManager.CheckFilesystem(ctx); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
			return
		}
	} else if u.cfg != nil {
		fs, err := storage.NewFilesystemStorage(u.cfg.ArtifactRoot, u.cfg.StagingRoot)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
			return
		}
		if err := fs.CheckCapabilities(ctx); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
			return
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "Local filesystem read, write, and delete permissions verified successfully!",
	})
}

func (u *AdminUI) handleProductDownload(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("product_id")
	if productID == "" {
		http.Redirect(w, r, "/admin/products?flash_error=Missing+product_id+parameter", http.StatusFound)
		return
	}

	product, err := u.db.GetProduct(r.Context(), productID)
	if err != nil {
		http.Redirect(w, r, "/admin/products?flash_error=Product+not+found", http.StatusFound)
		return
	}

	versionStr := r.URL.Query().Get("version")
	var targetVersion *db.ProductVersion

	if versionStr != "" {
		versions, err := u.db.ListProductVersions(r.Context(), productID)
		if err == nil {
			for _, v := range versions {
				if v.Version == versionStr {
					targetVersion = v
					break
				}
			}
		}
	}

	if targetVersion == nil {
		cur, err := u.db.GetCurrentVersion(r.Context(), productID)
		if err != nil || cur == nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_error=No+active+download+available+for+%s", url.QueryEscape(product.DisplayName)), http.StatusFound)
			return
		}
		targetVersion = cur
	}

	if targetVersion.StoragePath == "" || targetVersion.IsDeleted {
		http.Redirect(w, r, "/admin/products?flash_error=Artifact+is+no+longer+available+in+storage", http.StatusFound)
		return
	}

	stream, _, err := u.storage.OpenArtifact(r.Context(), targetVersion.StoragePath)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_error=Failed+to+open+artifact:+%s", url.QueryEscape(err.Error())), http.StatusFound)
		return
	}
	defer stream.Close()

	etag := fmt.Sprintf(`"%s"`, targetVersion.SHA256)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, product.ArtifactFilename))
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")

	http.ServeContent(w, r, product.ArtifactFilename, targetVersion.ReleasedAt, stream)
}

func (u *AdminUI) handleProfile(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Profile", "profile")
	data := u.newViewData(base, nil)
	_ = u.parsedTemplates["profile"].Execute(w, data)
}

func (u *AdminUI) handleProfileChangePassword(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(adminUserKey).(*db.User)
	if user == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if currentPassword == "" || len(newPassword) < 8 {
		http.Redirect(w, r, "/admin/profile?flash_error=New+password+must+be+at+least+8+characters", http.StatusFound)
		return
	}

	if newPassword != confirmPassword {
		http.Redirect(w, r, "/admin/profile?flash_error=New+passwords+do+not+match", http.StatusFound)
		return
	}

	// Fetch full user record with password hash
	dbUser, err := u.db.GetUserByID(r.Context(), user.ID)
	if err != nil {
		http.Redirect(w, r, "/admin/profile?flash_error=Failed+to+retrieve+user+account", http.StatusFound)
		return
	}

	// Verify current password
	valid, err := auth.VerifyPassword(currentPassword, dbUser.PasswordHash)
	if err != nil || !valid {
		http.Redirect(w, r, "/admin/profile?flash_error=Incorrect+current+password", http.StatusFound)
		return
	}

	// Hash new password using Argon2id
	newHash, err := auth.HashPassword(newPassword)
	if err != nil {
		http.Redirect(w, r, "/admin/profile?flash_error=Failed+to+securely+hash+new+password", http.StatusFound)
		return
	}

	// Update user password in DB
	if err := u.db.UpdateUserPassword(r.Context(), user.ID, newHash); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/profile?flash_error=Failed+to+update+password:+%s", url.QueryEscape(err.Error())), http.StatusFound)
		return
	}

	http.Redirect(w, r, "/admin/profile?flash_success=Password+updated+successfully", http.StatusFound)
}


