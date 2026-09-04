package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AuditLog struct {
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	SourceIP      string    `json:"source_ip"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	DurationMs    int       `json:"duration_ms"`
	BytesSent     int64     `json:"bytes_sent"`
	APIKeyID      *string   `json:"api_key_id,omitempty"`
	ProductID     *string   `json:"product_id,omitempty"`
	Version       *string   `json:"version,omitempty"`
	UserAgent     string    `json:"user_agent"`
	FailureReason *string   `json:"failure_reason,omitempty"`
}

type DownloadStats struct {
	TotalRequests   int64 `json:"total_requests"`
	SuccessCount    int64 `json:"success_count"`
	BytesTransferred int64 `json:"bytes_transferred"`
	UniqueIPs       int64 `json:"unique_ips"`
}

func (d *DB) RecordAuditLog(ctx context.Context, l *AuditLog) error {
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.Timestamp.IsZero() {
		l.Timestamp = time.Now().UTC()
	}

	query := `INSERT INTO request_audit_logs (
		id, timestamp, source_ip, method, path, status, duration_ms, bytes_sent, api_key_id, product_id, version, user_agent, failure_reason
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := d.ExecContext(ctx, query, l.ID, l.Timestamp, l.SourceIP, l.Method, l.Path, l.Status, l.DurationMs, l.BytesSent, l.APIKeyID, l.ProductID, l.Version, l.UserAgent, l.FailureReason)
	return err
}

func (d *DB) ListAuditLogs(ctx context.Context, limit, offset int, product string) ([]*AuditLog, int, error) {
	countQuery := "SELECT COUNT(*) FROM request_audit_logs WHERE (product_id = ? OR ? = '')"
	var total int
	if err := d.QueryRowContext(ctx, countQuery, product, product).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, timestamp, source_ip, method, path, status, duration_ms, bytes_sent, api_key_id, product_id, version, user_agent, failure_reason
		FROM request_audit_logs
		WHERE (product_id = ? OR ? = '')
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?`

	rows, err := d.QueryContext(ctx, query, product, product, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.Timestamp, &l.SourceIP, &l.Method, &l.Path, &l.Status, &l.DurationMs, &l.BytesSent, &l.APIKeyID, &l.ProductID, &l.Version, &l.UserAgent, &l.FailureReason); err != nil {
			return nil, 0, err
		}
		logs = append(logs, &l)
	}
	return logs, total, rows.Err()
}

func (d *DB) PurgeOldAuditLogs(ctx context.Context, retentionDays int) (int64, error) {
	threshold := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	query := "DELETE FROM request_audit_logs WHERE timestamp < ?"
	res, err := d.ExecContext(ctx, query, threshold)
	if err != nil {
		return 0, fmt.Errorf("failed to purge audit logs: %w", err)
	}
	return res.RowsAffected()
}

func (d *DB) GetDownloadStats(ctx context.Context, hours int) (*DownloadStats, error) {
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	query := `SELECT 
		COUNT(*),
		COALESCE(SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(bytes_sent), 0),
		COUNT(DISTINCT source_ip)
		FROM request_audit_logs
		WHERE timestamp >= ?`

	row := d.QueryRowContext(ctx, query, since)
	var s DownloadStats
	err := row.Scan(&s.TotalRequests, &s.SuccessCount, &s.BytesTransferred, &s.UniqueIPs)
	if err != nil {
		return nil, err
	}
	return &s, nil
}
