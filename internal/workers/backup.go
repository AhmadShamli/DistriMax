package workers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

// CreateBackup generates an online SQLite snapshot via VACUUM INTO and rotates older snapshots.
func CreateBackup(ctx context.Context, database *db.DB, backupRoot string, s3Backend storage.StorageBackend, retentionCount int) (string, error) {
	if retentionCount <= 0 {
		retentionCount = 7
	}
	if err := os.MkdirAll(backupRoot, 0750); err != nil {
		return "", fmt.Errorf("failed to create backup root: %w", err)
	}

	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("distrimax-%s.sqlite3", timestamp)
	localPath := filepath.Join(backupRoot, filename)

	// 1. Hot snapshot via SQLite VACUUM INTO
	if err := database.VacuumInto(ctx, localPath); err != nil {
		return "", fmt.Errorf("vacuum into failed: %w", err)
	}

	// 2. Upload to S3 if enabled
	if s3Backend != nil {
		f, err := os.Open(localPath)
		if err == nil {
			info, _ := f.Stat()
			_, _ = s3Backend.StoreArtifact(ctx, "backups", timestamp, filename, f, info.Size())
			_ = f.Close()
		}
	}

	// 3. Rotate local backups to keep only last retentionCount
	rotateLocalBackups(backupRoot, retentionCount)

	return localPath, nil
}

func rotateLocalBackups(backupRoot string, keep int) {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return
	}

	var snapshots []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "distrimax-") && strings.HasSuffix(e.Name(), ".sqlite3") {
			snapshots = append(snapshots, filepath.Join(backupRoot, e.Name()))
		}
	}

	sort.Strings(snapshots)
	if len(snapshots) > keep {
		toDelete := snapshots[:len(snapshots)-keep]
		for _, path := range toDelete {
			_ = os.Remove(path)
		}
	}
}
