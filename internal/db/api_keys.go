package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAPIKeyNotFound = errors.New("api key not found")
)

type APIKey struct {
	ID              string     `json:"id"`
	KeyPrefix       string     `json:"key_prefix"`
	KeyHash         string     `json:"-"`
	DisplayName     string     `json:"display_name"`
	Scopes          string     `json:"scopes"`           // e.g. "manifest:read,download"
	AllowedProducts string     `json:"allowed_products"` // e.g. "*" or "geolite-city,geolite-country"
	RateLimitPerMin int        `json:"rate_limit_per_min"`
	IsRevoked       bool       `json:"is_revoked"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP      *string    `json:"last_used_ip,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (d *DB) CreateAPIKey(ctx context.Context, k *APIKey) error {
	if k.ID == "" {
		k.ID = uuid.NewString()
	}
	k.CreatedAt = time.Now().UTC()

	query := `INSERT INTO api_keys (
		id, key_prefix, key_hash, display_name, scopes, allowed_products, rate_limit_per_min, is_revoked, expires_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`

	_, err := d.ExecContext(ctx, query, k.ID, k.KeyPrefix, k.KeyHash, k.DisplayName, k.Scopes, k.AllowedProducts, k.RateLimitPerMin, k.ExpiresAt, k.CreatedAt)
	return err
}

func (d *DB) GetAPIKeysByPrefix(ctx context.Context, prefix string) ([]*APIKey, error) {
	query := `SELECT id, key_prefix, key_hash, display_name, scopes, allowed_products, rate_limit_per_min, is_revoked, expires_at, last_used_at, last_used_ip, created_at
		FROM api_keys WHERE key_prefix = ? AND is_revoked = 0`

	rows, err := d.QueryContext(ctx, query, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		var k APIKey
		var isRevoked int
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.KeyHash, &k.DisplayName, &k.Scopes, &k.AllowedProducts, &k.RateLimitPerMin, &isRevoked, &k.ExpiresAt, &k.LastUsedAt, &k.LastUsedIP, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.IsRevoked = (isRevoked == 1)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

func (d *DB) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	query := `SELECT id, key_prefix, key_hash, display_name, scopes, allowed_products, rate_limit_per_min, is_revoked, expires_at, last_used_at, last_used_ip, created_at
		FROM api_keys ORDER BY created_at DESC`

	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		var k APIKey
		var isRevoked int
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.KeyHash, &k.DisplayName, &k.Scopes, &k.AllowedProducts, &k.RateLimitPerMin, &isRevoked, &k.ExpiresAt, &k.LastUsedAt, &k.LastUsedIP, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.IsRevoked = (isRevoked == 1)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

func (d *DB) UpdateAPIKeyUsage(ctx context.Context, id, ip string) error {
	query := `UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP, last_used_ip = ? WHERE id = ?`
	_, err := d.ExecContext(ctx, query, ip, id)
	return err
}

func (d *DB) RevokeAPIKey(ctx context.Context, id string) error {
	query := `UPDATE api_keys SET is_revoked = 1 WHERE id = ?`
	res, err := d.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}
