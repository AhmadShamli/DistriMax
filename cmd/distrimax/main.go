package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/api"
	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/cache"
	"github.com/AhmadShamli/DistriMax/internal/config"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
	"github.com/AhmadShamli/DistriMax/internal/syncer"
	"github.com/AhmadShamli/DistriMax/internal/ui"
	"github.com/AhmadShamli/DistriMax/internal/workers"
)

func main() {
	log.Println("[DistriMax] Starting Internal MaxMind MMDB Distribution Server...")

	// 1. Load configuration
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("[DistriMax] Failed to load configuration: %v", err)
	}

	if err := cfg.EnsureDirectories(); err != nil {
		log.Fatalf("[DistriMax] Failed to prepare storage directories: %v", err)
	}

	// 2. Open SQLite Database and run schema migrations
	database, err := db.Open(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("[DistriMax] Failed to connect to SQLite: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.RunMigrations(ctx, database.DB); err != nil {
		log.Fatalf("[DistriMax] Failed to run schema migrations: %v", err)
	}
	log.Println("[DistriMax] Database schema verified and up-to-date")

	// 3. Initialize Storage Backend (defaults to filesystem; managed via Admin UI)
	fsStore, err := storage.NewFilesystemStorage(cfg.ArtifactRoot, cfg.StagingRoot)
	if err != nil {
		log.Fatalf("[DistriMax] Failed to init filesystem storage: %v", err)
	}
	store := storage.NewStorageManager(fsStore)
	log.Println("[DistriMax] Storage manager initialized with local filesystem backend")

	// 4. In-Memory Manifest Cache
	manifestCache := cache.NewManifestCache(60 * time.Second)

	// 5. Upstream MaxMind Client & Sync Engine
	maxmindClient := syncer.NewMaxMindClient("", 10*time.Minute)
	syncEngine := syncer.NewSyncer(database, store, maxmindClient, cfg.SettingsEncryptionKey, func(pv *db.ProductVersion) {
		log.Printf("[DistriMax] Invalidating manifest cache for product %s (version: %s)", pv.ProductID, pv.Version)
		manifestCache.Invalidate(pv.ProductID)

		// Dispatch Webhook notification asynchronously
		go func(version *db.ProductVersion) {
			webhookURL, _, err := database.GetSetting(context.Background(), "webhook_url")
			if err != nil || webhookURL == "" {
				return
			}
			encSecret, isEnc, err := database.GetSetting(context.Background(), "webhook_hmac_secret")
			hmacSecret := encSecret
			if err == nil && isEnc && len(cfg.SettingsEncryptionKey) == 32 {
				if dec, err := auth.Decrypt(encSecret, cfg.SettingsEncryptionKey); err == nil {
					hmacSecret = string(dec)
				}
			}

			event := &workers.WebhookEvent{
				Event:        "product.published",
				Product:      version.ProductID,
				Version:      version.Version,
				ReleasedAt:   version.ReleasedAt,
				SHA256:       version.SHA256,
				SizeBytes:    version.SizeBytes,
				DownloadPath: fmt.Sprintf("/v1/products/%s/download", version.ProductID),
			}

			ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancelTimeout()

			if err := workers.SendWebhook(ctxTimeout, database, webhookURL, hmacSecret, event); err != nil {
				log.Printf("[DistriMax] Webhook delivery failed for %s (%s): %v", version.ProductID, version.Version, err)
			} else {
				log.Printf("[DistriMax] Webhook delivered successfully for %s (%s)", version.ProductID, version.Version)
			}
		}(pv)
	})

	// 6. Background Scheduler
	scheduler := workers.NewScheduler(database, store, syncEngine, cfg.BackupRoot)
	go scheduler.Start(ctx)
	log.Println("[DistriMax] Background schedulers active (hourly cleanup, daily sync & backups)")

	// 7. Assemble Unified HTTP Router
	mux := http.NewServeMux()

	// Register Client APIs
	apiServer := api.NewServer(cfg, database, store, manifestCache)
	mux.Handle("/v1/", apiServer.Handler())
	mux.Handle("/health/", apiServer.Handler())
	mux.Handle("/status", apiServer.Handler())
	mux.Handle("/recover", apiServer.Handler())

	// Register Admin UI
	adminUI, err := ui.NewAdminUI(database, cfg, store, syncEngine)
	if err != nil {
		log.Fatalf("[DistriMax] Failed to initialize Admin UI: %v", err)
	}
	_ = adminUI.ReloadStorage(ctx)
	log.Printf("[DistriMax] Active storage driver: %s", store.ActiveDriver())
	adminUI.RegisterRoutes(mux)

	// Root redirect
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		setupDone, _, _ := database.GetSetting(r.Context(), "setup_completed")
		if setupDone != "true" {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
	})

	httpServer := &http.Server{
		Addr:         cfg.HTTPBindAddress,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Minute, // Allow large MMDB downloads to stream
		IdleTimeout:  60 * time.Second,
	}

	// 8. Graceful Immediate Lifecycle Shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[DistriMax] HTTP server listening on %s", cfg.HTTPBindAddress)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[DistriMax] HTTP server terminated: %v", err)
		}
	}()

	sig := <-shutdownChan
	log.Printf("[DistriMax] Received shutdown signal %s. Terminating immediately...", sig)
	cancel() // Stop background workers immediately

	_ = httpServer.Close() // Immediate close allowing clients to resume via HTTP Range
	log.Println("[DistriMax] Server stopped cleanly")
}
