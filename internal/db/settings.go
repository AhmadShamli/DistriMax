package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Setting struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	IsEncrypted bool      `json:"is_encrypted"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by"`
}

func (d *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var val string
	var isEnc int
	err := d.QueryRowContext(ctx, "SELECT value, is_encrypted FROM settings WHERE key = ?", key).Scan(&val, &isEnc)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return val, isEnc == 1, nil
}

func (d *DB) SetSetting(ctx context.Context, key, val string, isEncrypted bool, updatedBy string) error {
	isEnc := 0
	if isEncrypted {
		isEnc = 1
	}
	query := `INSERT INTO settings (key, value, is_encrypted, updated_at, updated_by)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			is_encrypted = excluded.is_encrypted,
			updated_at = CURRENT_TIMESTAMP,
			updated_by = excluded.updated_by`
	_, err := d.ExecContext(ctx, query, key, val, isEnc, updatedBy)
	return err
}

func (d *DB) ListSettings(ctx context.Context) ([]*Setting, error) {
	rows, err := d.QueryContext(ctx, "SELECT key, value, is_encrypted, updated_at, updated_by FROM settings ORDER BY key ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Setting
	for rows.Next() {
		var s Setting
		var isEnc int
		if err := rows.Scan(&s.Key, &s.Value, &isEnc, &s.UpdatedAt, &s.UpdatedBy); err != nil {
			return nil, err
		}
		s.IsEncrypted = (isEnc == 1)
		list = append(list, &s)
	}
	return list, rows.Err()
}
