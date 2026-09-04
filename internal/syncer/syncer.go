package syncer

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
	"github.com/google/uuid"
)

type Syncer struct {
	db              *db.DB
	storage         storage.StorageBackend
	maxmind         *MaxMindClient
	encryptionKey   []byte
	onPublish       func(pv *db.ProductVersion)
	locks           sync.Map // map[string]*sync.Mutex
}

type SyncResult struct {
	ProductID        string
	Version          string
	Status           string // "SUCCESS", "SKIPPED", "FAILED"
	DurationMs       int
	SHA256           string
	SizeBytes        int64
	ErrorMessage     string
}

func NewSyncer(database *db.DB, store storage.StorageBackend, client *MaxMindClient, encryptionKey []byte, onPublish func(pv *db.ProductVersion)) *Syncer {
	return &Syncer{
		db:            database,
		storage:       store,
		maxmind:       client,
		encryptionKey: encryptionKey,
		onPublish:     onPublish,
	}
}

// CheckCredentials checks MaxMind upstream credentials.
func (s *Syncer) CheckCredentials(ctx context.Context, accountID, licenseKey string) error {
	if s.maxmind == nil {
		return fmt.Errorf("maxmind client not initialized")
	}
	return s.maxmind.CheckCredentials(ctx, accountID, licenseKey)
}

// SyncProduct synchronizes an enabled product from upstream MaxMind.
func (s *Syncer) SyncProduct(ctx context.Context, productID string, triggerType string) (*SyncResult, error) {
	start := time.Now()

	// 1. Lock per product to prevent concurrent syncs
	lockInterface, _ := s.locks.LoadOrStore(productID, &sync.Mutex{})
	productLock := lockInterface.(*sync.Mutex)
	if !productLock.TryLock() {
		return nil, fmt.Errorf("sync already in progress for product %s", productID)
	}
	defer productLock.Unlock()

	// 2. Fetch product config
	product, err := s.db.GetProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("failed to load product: %w", err)
	}
	if !product.IsEnabled {
		return nil, fmt.Errorf("product %s is disabled", productID)
	}

	// 3. Retrieve credentials from settings
	accountID, _, err := s.db.GetSetting(ctx, "maxmind_account_id")
	if err != nil || accountID == "" {
		return nil, fmt.Errorf("maxmind_account_id is not configured")
	}

	encLicenseKey, isEnc, err := s.db.GetSetting(ctx, "maxmind_license_key")
	if err != nil || encLicenseKey == "" {
		return nil, fmt.Errorf("maxmind_license_key is not configured")
	}

	licenseKey := encLicenseKey
	if isEnc && len(s.encryptionKey) == 32 {
		decrypted, err := auth.Decrypt(encLicenseKey, s.encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt maxmind_license_key: %w", err)
		}
		licenseKey = string(decrypted)
	}

	// 4. Query current version for conditional download
	var ifModifiedSince *time.Time
	currentVersion, err := s.db.GetCurrentVersion(ctx, productID)
	if err == nil && currentVersion != nil {
		ifModifiedSince = &currentVersion.ReleasedAt
	}

	// 5. Download from MaxMind
	body, notModified, lastModified, err := s.maxmind.DownloadDatabase(ctx, accountID, licenseKey, product.EditionID, ifModifiedSince)
	if err != nil {
		s.recordSyncRun(ctx, productID, "FAILED", triggerType, "", int(time.Since(start).Milliseconds()), err.Error())
		return nil, err
	}

	if notModified {
		s.recordSyncRun(ctx, productID, "SKIPPED", triggerType, currentVersion.Version, int(time.Since(start).Milliseconds()), "")
		return &SyncResult{
			ProductID:  productID,
			Version:    currentVersion.Version,
			Status:     "SKIPPED",
			DurationMs: int(time.Since(start).Milliseconds()),
		}, nil
	}
	defer body.Close()

	// 6. Extract MMDB from tar.gz and compute SHA-256
	extractedBytes, sha256Hex, err := ExtractMMDBFromTarGz(body, product.ArtifactFilename)
	if err != nil {
		s.recordSyncRun(ctx, productID, "FAILED", triggerType, "", int(time.Since(start).Milliseconds()), err.Error())
		return nil, fmt.Errorf("failed to extract MMDB: %w", err)
	}

	// 7. Validate MMDB header & metadata
	meta, err := ValidateMMDB(extractedBytes, product.EditionID)
	if err != nil {
		s.recordSyncRun(ctx, productID, "FAILED", triggerType, "", int(time.Since(start).Milliseconds()), err.Error())
		return nil, fmt.Errorf("MMDB validation failed: %w", err)
	}

	// Version tag is UTC date of build epoch: YYYY-MM-DD
	versionTag := meta.BuildEpoch.Format("2006-01-02")
	if versionTag == "" {
		versionTag = lastModified.Format("2006-01-02")
	}

	// 8. Persist to storage backend
	backendName, _, _ := s.db.GetSetting(ctx, "storage_backend")
	if backendName == "" {
		backendName = "filesystem"
	}

	storagePath, err := s.storage.StoreArtifact(
		ctx,
		productID,
		versionTag,
		product.ArtifactFilename,
		bytes.NewReader(extractedBytes),
		int64(len(extractedBytes)),
	)
	if err != nil {
		s.recordSyncRun(ctx, productID, "FAILED", triggerType, versionTag, int(time.Since(start).Milliseconds()), err.Error())
		return nil, fmt.Errorf("failed to persist artifact to storage: %w", err)
	}

	// 9. Atomic publication in SQLite database
	pv := &db.ProductVersion{
		ProductID:      productID,
		Version:        versionTag,
		ReleasedAt:     meta.BuildEpoch,
		SHA256:         sha256Hex,
		SizeBytes:      int64(len(extractedBytes)),
		StorageBackend: backendName,
		StoragePath:    storagePath,
	}

	if err := s.db.PublishNewVersion(ctx, pv, product.RetentionDays); err != nil {
		s.recordSyncRun(ctx, productID, "FAILED", triggerType, versionTag, int(time.Since(start).Milliseconds()), err.Error())
		return nil, fmt.Errorf("failed to publish new version to database: %w", err)
	}

	// 10. Cache invalidation callback
	if s.onPublish != nil {
		s.onPublish(pv)
	}

	durationMs := int(time.Since(start).Milliseconds())
	s.recordSyncRun(ctx, productID, "SUCCESS", triggerType, versionTag, durationMs, "")

	return &SyncResult{
		ProductID:  productID,
		Version:    versionTag,
		Status:     "SUCCESS",
		DurationMs: durationMs,
		SHA256:     sha256Hex,
		SizeBytes:  int64(len(extractedBytes)),
	}, nil
}

func (s *Syncer) recordSyncRun(ctx context.Context, productID, status, triggerType, version string, durationMs int, errMsg string) {
	query := `INSERT INTO sync_runs (id, product_id, status, trigger_type, version_discovered, duration_ms, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`
	_, _ = s.db.ExecContext(ctx, query, uuid.NewString(), productID, status, triggerType, version, durationMs, errMsg)
}
