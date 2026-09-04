package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUserNotFound              = errors.New("user not found")
	ErrCannotDisableLastAdmin    = errors.New("cannot disable the last active administrator")
	ErrSessionNotFound           = errors.New("session not found")
	ErrSessionExpired            = errors.New("session expired")
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsDisabled   bool      `json:"is_disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Session struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

func (d *DB) CreateUser(ctx context.Context, username, passwordHash string) (*User, error) {
	u := &User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: passwordHash,
		IsDisabled:   false,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	query := `INSERT INTO users (id, username, password_hash, is_disabled, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?)`
	_, err := d.ExecContext(ctx, query, u.ID, u.Username, u.PasswordHash, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *DB) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	query := `SELECT id, username, password_hash, is_disabled, created_at, updated_at FROM users WHERE username = ?`
	row := d.QueryRowContext(ctx, query, username)

	var u User
	var isDisabled int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isDisabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	u.IsDisabled = (isDisabled == 1)
	return &u, nil
}

func (d *DB) GetUserByID(ctx context.Context, id string) (*User, error) {
	query := `SELECT id, username, password_hash, is_disabled, created_at, updated_at FROM users WHERE id = ?`
	row := d.QueryRowContext(ctx, query, id)

	var u User
	var isDisabled int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &isDisabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	u.IsDisabled = (isDisabled == 1)
	return &u, nil
}

func (d *DB) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := d.QueryContext(ctx, "SELECT id, username, password_hash, is_disabled, created_at, updated_at FROM users ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var u User
		var isDisabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &isDisabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.IsDisabled = (isDisabled == 1)
		users = append(users, &u)
	}
	return users, rows.Err()
}

func (d *DB) UpdateUserPassword(ctx context.Context, id, newHash string) error {
	_, err := d.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", newHash, id)
	return err
}

func (d *DB) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	if disabled {
		// Verify there will remain at least one active user
		var activeCount int
		err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_disabled = 0 AND id != ?", id).Scan(&activeCount)
		if err != nil {
			return err
		}
		if activeCount < 1 {
			return ErrCannotDisableLastAdmin
		}
	}

	val := 0
	if disabled {
		val = 1
	}
	_, err := d.ExecContext(ctx, "UPDATE users SET is_disabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", val, id)
	return err
}

// Session helpers

func (d *DB) CreateSession(ctx context.Context, sessionID, userID string, expiresAt time.Time) error {
	now := time.Now().UTC()
	query := `INSERT INTO sessions (id, user_id, expires_at, created_at, last_active_at)
		VALUES (?, ?, ?, ?, ?)`
	_, err := d.ExecContext(ctx, query, sessionID, userID, expiresAt, now, now)
	return err
}

func (d *DB) GetSession(ctx context.Context, sessionID string) (*Session, *User, error) {
	query := `SELECT s.id, s.user_id, s.expires_at, s.created_at, s.last_active_at,
		u.id, u.username, u.password_hash, u.is_disabled, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		WHERE s.id = ?`

	row := d.QueryRowContext(ctx, query, sessionID)

	var s Session
	var u User
	var isDisabled int
	err := row.Scan(
		&s.ID, &s.UserID, &s.ExpiresAt, &s.CreatedAt, &s.LastActiveAt,
		&u.ID, &u.Username, &u.PasswordHash, &isDisabled, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrSessionNotFound
		}
		return nil, nil, err
	}
	u.IsDisabled = (isDisabled == 1)

	if time.Now().UTC().After(s.ExpiresAt) {
		_ = d.RevokeSession(ctx, sessionID)
		return nil, nil, ErrSessionExpired
	}

	// Update last_active_at asynchronously or inline
	_, _ = d.ExecContext(ctx, "UPDATE sessions SET last_active_at = CURRENT_TIMESTAMP WHERE id = ?", sessionID)

	return &s, &u, nil
}

func (d *DB) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID)
	return err
}

func (d *DB) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

func (d *DB) CleanExpiredSessions(ctx context.Context) error {
	_, err := d.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP")
	return err
}
