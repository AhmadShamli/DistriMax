package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// RunMigrations discovers embedded SQL scripts and applies any unapplied forward migrations.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	// 1. Ensure schema_migrations exists
	initSQL := `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.ExecContext(ctx, initSQL); err != nil {
		return fmt.Errorf("failed to initialize schema_migrations table: %w", err)
	}

	// 2. Read embedded migrations
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations directory: %w", err)
	}

	type migrationEntry struct {
		version int
		name    string
		content []byte
		hash    string
	}

	var migrations []migrationEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		// Expect filename format: 0001_initial_schema.sql
		base := entry.Name()
		parts := strings.SplitN(base, "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("invalid migration filename version in %s: %w", base, err)
		}

		data, err := migrationFiles.ReadFile(filepath.Join("migrations", base))
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", base, err)
		}

		hash := sha256.Sum256(data)
		migrations = append(migrations, migrationEntry{
			version: v,
			name:    base,
			content: data,
			hash:    hex.EncodeToString(hash[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	// 3. Process each migration
	for _, m := range migrations {
		var existingHash string
		err := db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", m.version).Scan(&existingHash)
		if err == nil {
			// Already applied: verify checksum
			if existingHash != m.hash {
				return fmt.Errorf("checksum mismatch for migration %d (%s): recorded %s, current %s", m.version, m.name, existingHash, m.hash)
			}
			continue // Already up to date
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("failed to query migration status for %d: %w", m.version, err)
		}

		// Apply migration inside an atomic transaction
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration transaction for %d: %w", m.version, err)
		}

		if _, err := tx.ExecContext(ctx, string(m.content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %s: %w", m.name, err)
		}

		insertSQL := "INSERT INTO schema_migrations (version, name, checksum) VALUES (?, ?, ?)"
		if _, err := tx.ExecContext(ctx, insertSQL, m.version, m.name, m.hash); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %s: %w", m.name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", m.name, err)
		}
	}

	return nil
}
