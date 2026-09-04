package workers

import (
	"context"
	"fmt"
	"strconv"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

type CleanupResult struct {
	PurgedVersions  int   `json:"purged_versions"`
	PurgedAuditLogs int64 `json:"purged_audit_logs"`
}

// RunCleanup purges superseded versions older than retention policy (30 days) and old audit logs.
func RunCleanup(ctx context.Context, database *db.DB, store storage.StorageBackend) (*CleanupResult, error) {
	result := &CleanupResult{}

	// 1. Purge expired MMDB artifacts
	expiredVersions, err := database.GetExpiredVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query expired versions: %w", err)
	}

	for _, v := range expiredVersions {
		if v.StoragePath != "" {
			_ = store.DeleteArtifact(ctx, v.StoragePath)
		}
		if err := database.MarkVersionDeleted(ctx, v.ID); err == nil {
			result.PurgedVersions++
		}
	}

	// 2. Purge old audit logs
	retentionDays := 90
	if val, _, _ := database.GetSetting(ctx, "audit_retention_days"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			retentionDays = parsed
		}
	}

	purgedLogs, err := database.PurgeOldAuditLogs(ctx, retentionDays)
	if err == nil {
		result.PurgedAuditLogs = purgedLogs
	}

	// 3. Clean expired admin sessions
	_ = database.CleanExpiredSessions(ctx)

	return result, nil
}
