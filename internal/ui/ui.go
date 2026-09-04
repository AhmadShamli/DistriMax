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
	syncer          *syncer.Syncer
	parsedTemplates map[string]*template.Template
}

func NewAdminUI(database *db.DB, cfg *config.Config, store storage.StorageBackend, syncEngine *syncer.Syncer) (*AdminUI, error) {
	ui := &AdminUI{
		db:              database,
		cfg:             cfg,
		storage:         store,
		syncer:          syncEngine,
		parsedTemplates: make(map[string]*template.Template),
	}

	pages := []string{"dashboard", "products", "downloads", "apikeys", "operations", "audit", "users", "settings"}
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
	mux.HandleFunc("POST /admin/users/create", u.requireAuth(u.handleUserCreate))
	mux.HandleFunc("POST /admin/users/disable", u.requireAuth(u.handleUserDisable))
	mux.HandleFunc("POST /admin/users/enable", u.requireAuth(u.handleUserEnable))

	mux.HandleFunc("GET /admin/settings", u.requireAuth(u.handleSettings))
	mux.HandleFunc("POST /admin/settings/save", u.requireAuth(u.handleSettingsSave))
	mux.HandleFunc("POST /admin/settings/test-maxmind", u.requireAuth(u.handleTestMaxMind))
	mux.HandleFunc("POST /admin/settings/test-s3", u.requireAuth(u.handleTestS3))
	mux.HandleFunc("POST /admin/settings/test-webhook", u.requireAuth(u.handleTestWebhook))
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

	err = u.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO users (id, username, password_hash, is_disabled, created_at, updated_at) VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", "admin-1", username, hash)
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE settings SET value = 'true', updated_at = CURRENT_TIMESTAMP WHERE key = 'setup_completed'")
		return err
	})
	if err != nil {
		_ = u.parsedTemplates["setup"].Execute(w, map[string]string{"Error": "Failed to initialize database: " + err.Error()})
		return
	}

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
}

func (u *AdminUI) buildBaseData(r *http.Request, title, nav string) BaseViewData {
	user, _ := r.Context().Value(adminUserKey).(*db.User)
	csrf, _ := r.Context().Value(adminCSRFKey).(string)

	products, _ := u.db.ListProducts(r.Context())
	activeCount := 0
	health := "HEALTHY"
	for _, p := range products {
		if p.IsEnabled {
			activeCount++
			cur, err := u.db.GetCurrentVersion(r.Context(), p.ID)
			if err != nil || cur == nil {
				health = "DEGRADED"
			}
		}
	}

	return BaseViewData{
		Title:              title,
		ActiveNav:          nav,
		HealthStatus:       health,
		ActiveProductCount: activeCount,
		CurrentUser:        user,
		CSRFToken:          csrf,
		FlashSuccess:       r.URL.Query().Get("flash_success"),
		FlashError:         r.URL.Query().Get("flash_error"),
	}
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

	data := map[string]interface{}{
		"BaseViewData":   base,
		"Products":       pList,
		"Stats":          stats,
		"StorageBackend": strings.ToUpper(storageBackend),
		"SVGPoints":      svgPoints,
		"CSRFToken":      base.CSRFToken,
	}

	_ = u.parsedTemplates["dashboard"].Execute(w, data)
}

func (u *AdminUI) handleProducts(w http.ResponseWriter, r *http.Request) {
	base := u.buildBaseData(r, "Products", "products")
	products, _ := u.db.ListProducts(r.Context())

	type productWithVersions struct {
		Product  *db.Product
		Versions []*db.ProductVersion
	}
	var list []productWithVersions
	for _, p := range products {
		versions, _ := u.db.ListProductVersions(r.Context(), p.ID)
		list = append(list, productWithVersions{Product: p, Versions: versions})
	}

	data := map[string]interface{}{
		"BaseViewData": base,
		"Products":     list,
		"CSRFToken":    base.CSRFToken,
	}
	_ = u.parsedTemplates["products"].Execute(w, data)
}

func (u *AdminUI) handleProductSync(w http.ResponseWriter, r *http.Request) {
	productID := r.FormValue("product_id")
	res, err := u.syncer.SyncProduct(r.Context(), productID, "MANUAL")
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_error=Sync+failed:+%s", err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/products?flash_success=Sync+completed:+%s+(%s)", res.ProductID, res.Status), http.StatusFound)
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

	data := map[string]interface{}{
		"BaseViewData": base,
		"Keys":         keys,
		"NewKeySecret": r.URL.Query().Get("new_key_secret"),
		"CSRFToken":    base.CSRFToken,
	}
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

	data := map[string]interface{}{
		"BaseViewData":    base,
		"Stats":           stats,
		"RecentDownloads": logs,
		"CSRFToken":       base.CSRFToken,
	}
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

	data := map[string]interface{}{
		"BaseViewData":    base,
		"Logs":            logs,
		"CurrentPage":     page,
		"PrevPage":        page - 1,
		"NextPage":        page + 1,
		"HasNextPage":     (page * limit) < total,
		"TotalCount":      total,
		"SelectedProduct": product,
		"CSRFToken":       base.CSRFToken,
	}
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

	data := map[string]interface{}{
		"BaseViewData":      base,
		"SyncRuns":          runs,
		"WebhookDeliveries": deliveries,
		"CSRFToken":         base.CSRFToken,
	}
	_ = u.parsedTemplates["operations"].Execute(w, data)
}

func (u *AdminUI) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	products, _ := u.db.ListProducts(r.Context())
	for _, p := range products {
		if p.IsEnabled {
			_, _ = u.syncer.SyncProduct(r.Context(), p.ID, "MANUAL")
		}
	}
	http.Redirect(w, r, "/admin/operations?flash_success=Upstream+sync+triggered+for+all+enabled+products", http.StatusFound)
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

	data := map[string]interface{}{
		"BaseViewData": base,
		"Users":        users,
		"CSRFToken":    base.CSRFToken,
	}
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

	data := map[string]interface{}{
		"BaseViewData": base,
		"Settings":     settingsMap,
		"CSRFToken":    base.CSRFToken,
	}
	_ = u.parsedTemplates["settings"].Execute(w, data)
}

func (u *AdminUI) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(adminUserKey).(*db.User)
	updatedBy := "admin"
	if user != nil {
		updatedBy = user.Username
	}

	keys := []string{
		"maxmind_account_id", "storage_backend", "s3_endpoint", "s3_bucket",
		"s3_region", "s3_access_key_id", "s3_force_path_style", "sync_schedule_cron",
		"staleness_threshold_days", "artifact_retention_days", "audit_retention_days",
		"webhook_url",
	}

	for _, k := range keys {
		val := r.FormValue(k)
		if val != "" {
			_ = u.db.SetSetting(r.Context(), k, val, false, updatedBy)
		}
	}

	// Encrypted Secrets
	if secret := r.FormValue("maxmind_license_key"); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "maxmind_license_key", enc, true, updatedBy)
	}

	if secret := r.FormValue("s3_secret_access_key"); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "s3_secret_access_key", enc, true, updatedBy)
	}

	if secret := r.FormValue("webhook_hmac_secret"); secret != "" && len(u.cfg.SettingsEncryptionKey) == 32 {
		enc, _ := auth.Encrypt([]byte(secret), u.cfg.SettingsEncryptionKey)
		_ = u.db.SetSetting(r.Context(), "webhook_hmac_secret", enc, true, updatedBy)
	}

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
