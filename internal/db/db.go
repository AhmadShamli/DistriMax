package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps standard sql.DB with application helpers.
type DB struct {
	*sql.DB
}

// Open initializes and configures a SQLite connection pool with production PRAGMAs.
func Open(sqlitePath string) (*DB, error) {
	if sqlitePath != ":memory:" {
		dir := filepath.Dir(sqlitePath)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create sqlite directory: %w", err)
		}
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", sqlitePath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite is a single active writer database. Keeping a single open connection
	// prevents locking contention while WAL mode enables concurrent reads.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Verify connection
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping sqlite database: %w", err)
	}

	return &DB{DB: db}, nil
}

// WithTx executes an operation inside an explicit SQLite transaction.
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// VacuumInto creates a hot, non-blocking online backup snapshot using SQLite VACUUM INTO.
func (d *DB) VacuumInto(ctx context.Context, backupPath string) error {
	dir := filepath.Dir(backupPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Remove target if it already exists, because VACUUM INTO fails if destination exists
	_ = os.Remove(backupPath)

	query := fmt.Sprintf("VACUUM INTO '%s'", backupPath)
	_, err := d.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create backup snapshot: %w", err)
	}
	return nil
}
