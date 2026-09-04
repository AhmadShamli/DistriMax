package workers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/google/uuid"
)

type WebhookEvent struct {
	Event        string    `json:"event"`
	EventID      string    `json:"event_id"`
	OccurredAt   time.Time `json:"occurred_at"`
	Product      string    `json:"product"`
	Version      string    `json:"version"`
	ReleasedAt   time.Time `json:"released_at"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	DownloadPath string    `json:"download_path"`
}

func SendWebhook(ctx context.Context, database *db.DB, targetURL, hmacSecret string, event *WebhookEvent) error {
	if targetURL == "" {
		return nil
	}

	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook event: %w", err)
	}

	timestamp := event.OccurredAt.Unix()
	var signatureHeader string
	if hmacSecret != "" {
		mac := hmac.New(sha256.New, []byte(hmacSecret))
		mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
		mac.Write(payload)
		signature := hex.EncodeToString(mac.Sum(nil))
		signatureHeader = fmt.Sprintf("t=%d,v1=%s", timestamp, signature)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	maxRetries := 5
	backoff := 100 * time.Millisecond

	var lastErr error
	var statusCode int

	for attempt := 1; attempt <= maxRetries; attempt++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", "DistriMax-Webhook/1.0")
		if signatureHeader != "" {
			req.Header.Set("DistriMax-Signature", signatureHeader)
		}

		resp, err := client.Do(req)
		durationMs := int(time.Since(start).Milliseconds())

		if err == nil {
			statusCode = resp.StatusCode
			_ = resp.Body.Close()
			if statusCode >= 200 && statusCode < 300 {
				recordWebhookDelivery(ctx, database, event, targetURL, statusCode, attempt, durationMs, "")
				return nil
			}
			lastErr = fmt.Errorf("webhook endpoint returned HTTP %d", statusCode)
		} else {
			lastErr = err
		}

		// Don't retry client errors (4xx) except 429
		if statusCode >= 400 && statusCode < 500 && statusCode != http.StatusTooManyRequests {
			break
		}

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	recordWebhookDelivery(ctx, database, event, targetURL, statusCode, maxRetries, 0, lastErr.Error())
	return lastErr
}

func recordWebhookDelivery(ctx context.Context, database *db.DB, event *WebhookEvent, targetURL string, status, attempt, durationMs int, errMsg string) {
	if database == nil {
		return
	}
	query := `INSERT INTO webhook_deliveries (id, event_type, product_id, version, target_url, status_code, attempt_count, duration_ms, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`
	_, _ = database.ExecContext(ctx, query, uuid.NewString(), event.Event, event.Product, event.Version, targetURL, status, attempt, durationMs, errMsg)
}
