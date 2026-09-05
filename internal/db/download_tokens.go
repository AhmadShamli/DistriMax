package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrTokenNotFound = errors.New("download token not found")
	ErrTokenExpired  = errors.New("download token has expired")
	ErrTokenRevoked  = errors.New("download token has been revoked")
)

type DownloadToken struct {
	Token         string    `json:"token"`
	ProductID     string    `json:"product_id"`
	Version       string    `json:"version"` // optional: empty means current version
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	DownloadCount int       `json:"download_count"`
	IsRevoked     bool      `json:"is_revoked"`
}

// CreateDownloadToken stores a newly generated temporary download token in SQLite.
func (d *DB) CreateDownloadToken(ctx context.Context, dt *DownloadToken) error {
	if dt.CreatedAt.IsZero() {
		dt.CreatedAt = time.Now().UTC()
	}

	query := `INSERT INTO download_tokens (
		token, product_id, version, created_by, created_at, expires_at, download_count, is_revoked
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	isRevoked := 0
	if dt.IsRevoked {
		isRevoked = 1
	}

	_, err := d.ExecContext(ctx, query, dt.Token, dt.ProductID, dt.Version, dt.CreatedBy, dt.CreatedAt, dt.ExpiresAt, dt.DownloadCount, isRevoked)
	return err
}

// GetDownloadToken retrieves a download token by its token string.
func (d *DB) GetDownloadToken(ctx context.Context, token string) (*DownloadToken, error) {
	query := `SELECT token, product_id, version, created_by, created_at, expires_at, download_count, is_revoked
		FROM download_tokens WHERE token = ?`

	row := d.QueryRowContext(ctx, query, token)

	var dt DownloadToken
	var isRevoked int
	err := row.Scan(&dt.Token, &dt.ProductID, &dt.Version, &dt.CreatedBy, &dt.CreatedAt, &dt.ExpiresAt, &dt.DownloadCount, &isRevoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	dt.IsRevoked = (isRevoked == 1)
	return &dt, nil
}

// IncrementDownloadTokenUsage increments the download counter for a token.
func (d *DB) IncrementDownloadTokenUsage(ctx context.Context, token string) error {
	query := `UPDATE download_tokens SET download_count = download_count + 1 WHERE token = ?`
	_, err := d.ExecContext(ctx, query, token)
	return err
}

// RevokeDownloadToken marks a download token as revoked.
func (d *DB) RevokeDownloadToken(ctx context.Context, token string) error {
	query := `UPDATE download_tokens SET is_revoked = 1 WHERE token = ?`
	_, err := d.ExecContext(ctx, query, token)
	return err
}

// CleanExpiredDownloadTokens deletes expired or revoked download tokens.
func (d *DB) CleanExpiredDownloadTokens(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	query := `DELETE FROM download_tokens WHERE expires_at < ? OR is_revoked = 1`
	res, err := d.ExecContext(ctx, query, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
