package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrProductNotFound = errors.New("product not found")
	ErrVersionNotFound = errors.New("product version not found")
)

type Product struct {
	ID                string    `json:"id"`
	EditionID         string    `json:"edition_id"`
	DisplayName       string    `json:"display_name"`
	ArtifactFilename  string    `json:"artifact_filename"`
	IsEnabled         bool      `json:"is_enabled"`
	SyncScheduleCron  string    `json:"sync_schedule_cron"`
	RetentionDays     int       `json:"retention_days"`
	StalenessDays     int       `json:"staleness_days"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ProductVersion struct {
	ID              string     `json:"id"`
	ProductID       string     `json:"product_id"`
	Version         string     `json:"version"`
	ReleasedAt      time.Time  `json:"released_at"`
	SHA256          string     `json:"sha256"`
	SizeBytes       int64      `json:"size_bytes"`
	StorageBackend  string     `json:"storage_backend"`
	StoragePath     string     `json:"storage_path"`
	IsCurrent       bool       `json:"is_current"`
	SupersededAt    *time.Time `json:"superseded_at,omitempty"`
	CleanupDeadline *time.Time `json:"cleanup_deadline,omitempty"`
	IsDeleted       bool       `json:"is_deleted"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (d *DB) GetProduct(ctx context.Context, id string) (*Product, error) {
	query := `SELECT id, edition_id, display_name, artifact_filename, is_enabled, sync_schedule_cron, retention_days, staleness_days, created_at, updated_at
		FROM products WHERE id = ?`
	row := d.QueryRowContext(ctx, query, id)

	var p Product
	var isEnabled int
	err := row.Scan(&p.ID, &p.EditionID, &p.DisplayName, &p.ArtifactFilename, &isEnabled, &p.SyncScheduleCron, &p.RetentionDays, &p.StalenessDays, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	p.IsEnabled = (isEnabled == 1)
	return &p, nil
}

func (d *DB) ListProducts(ctx context.Context) ([]*Product, error) {
	query := `SELECT id, edition_id, display_name, artifact_filename, is_enabled, sync_schedule_cron, retention_days, staleness_days, created_at, updated_at
		FROM products ORDER BY id ASC`
	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []*Product
	for rows.Next() {
		var p Product
		var isEnabled int
		if err := rows.Scan(&p.ID, &p.EditionID, &p.DisplayName, &p.ArtifactFilename, &isEnabled, &p.SyncScheduleCron, &p.RetentionDays, &p.StalenessDays, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.IsEnabled = (isEnabled == 1)
		products = append(products, &p)
	}
	return products, rows.Err()
}

func (d *DB) GetCurrentVersion(ctx context.Context, productID string) (*ProductVersion, error) {
	query := `SELECT id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, superseded_at, cleanup_deadline, is_deleted, created_at
		FROM product_versions WHERE product_id = ? AND is_current = 1 AND is_deleted = 0 LIMIT 1`
	row := d.QueryRowContext(ctx, query, productID)

	var v ProductVersion
	var isCurrent, isDeleted int
	err := row.Scan(&v.ID, &v.ProductID, &v.Version, &v.ReleasedAt, &v.SHA256, &v.SizeBytes, &v.StorageBackend, &v.StoragePath, &isCurrent, &v.SupersededAt, &v.CleanupDeadline, &isDeleted, &v.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	v.IsCurrent = (isCurrent == 1)
	v.IsDeleted = (isDeleted == 1)
	return &v, nil
}

func (d *DB) GetProductVersion(ctx context.Context, productID, version string) (*ProductVersion, error) {
	query := `SELECT id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, superseded_at, cleanup_deadline, is_deleted, created_at
		FROM product_versions WHERE product_id = ? AND version = ? AND is_deleted = 0 LIMIT 1`
	row := d.QueryRowContext(ctx, query, productID, version)

	var v ProductVersion
	var isCurrent, isDeleted int
	err := row.Scan(&v.ID, &v.ProductID, &v.Version, &v.ReleasedAt, &v.SHA256, &v.SizeBytes, &v.StorageBackend, &v.StoragePath, &isCurrent, &v.SupersededAt, &v.CleanupDeadline, &isDeleted, &v.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	v.IsCurrent = (isCurrent == 1)
	v.IsDeleted = (isDeleted == 1)
	return &v, nil
}

func (d *DB) ListProductVersions(ctx context.Context, productID string) ([]*ProductVersion, error) {
	query := `SELECT id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, superseded_at, cleanup_deadline, is_deleted, created_at
		FROM product_versions WHERE product_id = ? AND is_deleted = 0 ORDER BY released_at DESC`
	rows, err := d.QueryContext(ctx, query, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []*ProductVersion
	for rows.Next() {
		var v ProductVersion
		var isCurrent, isDeleted int
		if err := rows.Scan(&v.ID, &v.ProductID, &v.Version, &v.ReleasedAt, &v.SHA256, &v.SizeBytes, &v.StorageBackend, &v.StoragePath, &isCurrent, &v.SupersededAt, &v.CleanupDeadline, &isDeleted, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.IsCurrent = (isCurrent == 1)
		v.IsDeleted = (isDeleted == 1)
		versions = append(versions, &v)
	}
	return versions, rows.Err()
}

// PublishNewVersion sets the new version as current, superseding any prior version atomically.
func (d *DB) PublishNewVersion(ctx context.Context, v *ProductVersion, retentionDays int) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		cleanupDeadline := now.Add(time.Duration(retentionDays) * 24 * time.Hour)

		// 1. Mark existing current versions for this product as superseded
		supersedeSQL := `UPDATE product_versions 
			SET is_current = 0, superseded_at = ?, cleanup_deadline = ?
			WHERE product_id = ? AND is_current = 1`
		if _, err := tx.ExecContext(ctx, supersedeSQL, now, cleanupDeadline, v.ProductID); err != nil {
			return fmt.Errorf("failed to supersede prior versions: %w", err)
		}

		// 2. Insert or replace new version as current
		insertSQL := `INSERT INTO product_versions (
			id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, is_deleted, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)
		ON CONFLICT(product_id, version) DO UPDATE SET
			sha256 = excluded.sha256,
			size_bytes = excluded.size_bytes,
			storage_backend = excluded.storage_backend,
			storage_path = excluded.storage_path,
			is_current = 1,
			superseded_at = NULL,
			cleanup_deadline = NULL,
			is_deleted = 0`

		v.ID = fmt.Sprintf("%s:%s", v.ProductID, v.Version)
		_, err := tx.ExecContext(ctx, insertSQL, v.ID, v.ProductID, v.Version, v.ReleasedAt, v.SHA256, v.SizeBytes, v.StorageBackend, v.StoragePath, now)
		return err
	})
}

// RollbackToVersion promotes a retained historical version back to current.
func (d *DB) RollbackToVersion(ctx context.Context, productID, version string, retentionDays int) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		cleanupDeadline := now.Add(time.Duration(retentionDays) * 24 * time.Hour)

		// Check target version exists and is not deleted
		var count int
		err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM product_versions WHERE product_id = ? AND version = ? AND is_deleted = 0", productID, version).Scan(&count)
		if err != nil || count == 0 {
			return ErrVersionNotFound
		}

		// Demote existing current
		_, err = tx.ExecContext(ctx, "UPDATE product_versions SET is_current = 0, superseded_at = ?, cleanup_deadline = ? WHERE product_id = ? AND is_current = 1", now, cleanupDeadline, productID)
		if err != nil {
			return err
		}

		// Promote rollback version
		_, err = tx.ExecContext(ctx, "UPDATE product_versions SET is_current = 1, superseded_at = NULL, cleanup_deadline = NULL WHERE product_id = ? AND version = ?", productID, version)
		return err
	})
}

// GetExpiredVersions returns versions whose cleanup deadline has passed.
func (d *DB) GetExpiredVersions(ctx context.Context) ([]*ProductVersion, error) {
	now := time.Now().UTC()
	query := `SELECT id, product_id, version, released_at, sha256, size_bytes, storage_backend, storage_path, is_current, superseded_at, cleanup_deadline, is_deleted, created_at
		FROM product_versions 
		WHERE is_current = 0 AND is_deleted = 0 AND cleanup_deadline IS NOT NULL AND cleanup_deadline <= ?`

	rows, err := d.QueryContext(ctx, query, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expired []*ProductVersion
	for rows.Next() {
		var v ProductVersion
		var isCurrent, isDeleted int
		if err := rows.Scan(&v.ID, &v.ProductID, &v.Version, &v.ReleasedAt, &v.SHA256, &v.SizeBytes, &v.StorageBackend, &v.StoragePath, &isCurrent, &v.SupersededAt, &v.CleanupDeadline, &isDeleted, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.IsCurrent = (isCurrent == 1)
		v.IsDeleted = (isDeleted == 1)
		expired = append(expired, &v)
	}
	return expired, rows.Err()
}

// MarkVersionDeleted updates metadata after physical deletion.
func (d *DB) MarkVersionDeleted(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, "UPDATE product_versions SET is_deleted = 1, storage_path = '' WHERE id = ?", id)
	return err
}

// UpdateProductsDefaults updates the default sync schedule, staleness days, and retention days across products.
func (d *DB) UpdateProductsDefaults(ctx context.Context, cron string, stalenessDays, retentionDays int) error {
	query := `UPDATE products SET
		sync_schedule_cron = CASE WHEN ? != '' THEN ? ELSE sync_schedule_cron END,
		staleness_days = CASE WHEN ? > 0 THEN ? ELSE staleness_days END,
		retention_days = CASE WHEN ? > 0 THEN ? ELSE retention_days END,
		updated_at = CURRENT_TIMESTAMP`
	_, err := d.ExecContext(ctx, query, cron, cron, stalenessDays, stalenessDays, retentionDays, retentionDays)
	return err
}

