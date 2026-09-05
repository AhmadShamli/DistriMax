package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/auth"
	"github.com/AhmadShamli/DistriMax/internal/db"
)

type contextKey string

const (
	APIKeyContextKey        contextKey = "apiKey"
	DownloadTokenContextKey contextKey = "downloadToken"
)

// JSONError sends a consistent JSON error envelope.
func JSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}

// SanitizeURL scrubs sensitive parameters from request URLs.
func SanitizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	for key := range q {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
			q.Set(key, "[REDACTED]")
		}
	}
	clean := *u
	clean.RawQuery = q.Encode()
	return clean.RequestURI()
}

type responseWriterWrapper struct {
	http.ResponseWriter
	status      int
	bytesSent   int64
	wroteHeader bool
}

func (rw *responseWriterWrapper) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.status = code
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriterWrapper) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesSent += int64(n)
	return n, err
}

// AuditLoggerMiddleware logs all HTTP requests into SQLite database and sanitizes telemetry.
func AuditLoggerMiddleware(database *db.DB, isTrustedProxy func(net.IP) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			clientIP := ResolveClientIP(r, isTrustedProxy)
			sanitizedPath := SanitizeURL(r.URL)

			rw := &responseWriterWrapper{
				ResponseWriter: w,
				status:         http.StatusOK,
			}

			next.ServeHTTP(rw, r)

			duration := int(time.Since(start).Milliseconds())

			var apiKeyID *string
			if k, ok := r.Context().Value(APIKeyContextKey).(*db.APIKey); ok && k != nil {
				apiKeyID = &k.ID
			}

			_ = database.RecordAuditLog(r.Context(), &db.AuditLog{
				Timestamp:  start.UTC(),
				SourceIP:   clientIP,
				Method:     r.Method,
				Path:       sanitizedPath,
				Status:     rw.status,
				DurationMs: duration,
				BytesSent:  rw.bytesSent,
				APIKeyID:   apiKeyID,
				UserAgent:  r.UserAgent(),
			})
		})
	}
}

// RequireAPIKey verifies API key authentication from Bearer header, X-API-Key, or ?api_key= query parameter.
func RequireAPIKey(database *db.DB, requiredScope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				JSONError(w, http.StatusUnauthorized, "unauthorized", "API key is required via Authorization Bearer header, X-API-Key, or api_key parameter")
				return
			}

			prefix := auth.ExtractPrefix(token)
			candidates, err := database.GetAPIKeysByPrefix(r.Context(), prefix)
			if err != nil || len(candidates) == 0 {
				JSONError(w, http.StatusUnauthorized, "invalid_api_key", "The provided API key is invalid or revoked")
				return
			}

			var matchedKey *db.APIKey
			for _, k := range candidates {
				match, err := auth.VerifyPassword(token, k.KeyHash)
				if err == nil && match {
					matchedKey = k
					break
				}
			}

			if matchedKey == nil || matchedKey.IsRevoked {
				JSONError(w, http.StatusUnauthorized, "invalid_api_key", "The provided API key is invalid or revoked")
				return
			}

			if matchedKey.ExpiresAt != nil && time.Now().UTC().After(*matchedKey.ExpiresAt) {
				JSONError(w, http.StatusUnauthorized, "api_key_expired", "The provided API key has expired")
				return
			}

			// Verify required scope
			if requiredScope != "" && !hasScope(matchedKey.Scopes, requiredScope) {
				JSONError(w, http.StatusForbidden, "insufficient_permissions", "The provided API key lacks the required scope for this operation")
				return
			}

			// Verify product restrictions if product is in URL path
			productID := r.PathValue("product")
			if productID != "" && matchedKey.AllowedProducts != "*" {
				allowed := strings.Split(matchedKey.AllowedProducts, ",")
				matchedProduct := false
				for _, p := range allowed {
					if strings.TrimSpace(p) == productID {
						matchedProduct = true
						break
					}
				}
				if !matchedProduct {
					JSONError(w, http.StatusForbidden, "product_access_denied", "The provided API key is not authorized for this product")
					return
				}
			}

			// Update usage asynchronously
			clientIP := r.RemoteAddr
			go func(id, ip string) {
				_ = database.UpdateAPIKeyUsage(context.Background(), id, ip)
			}(matchedKey.ID, clientIP)

			ctx := context.WithValue(r.Context(), APIKeyContextKey, matchedKey)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireDownloadAuth verifies authentication for downloads via either:
// 1. Temporary direct download token (?token=... or ?download_token=...)
// 2. Permanent Client API Key (Bearer header, X-API-Key, or ?api_key=...)
func RequireDownloadAuth(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.URL.Query().Get("token")
			if token == "" {
				token = r.URL.Query().Get("download_token")
			}

			// If a temporary download token is provided, validate it
			if token != "" {
				dt, err := database.GetDownloadToken(r.Context(), token)
				if err != nil {
					JSONError(w, http.StatusUnauthorized, "invalid_token", "The provided download token is invalid or does not exist")
					return
				}

				if dt.IsRevoked {
					JSONError(w, http.StatusUnauthorized, "token_revoked", "The provided download token has been revoked")
					return
				}

				if time.Now().UTC().After(dt.ExpiresAt) {
					JSONError(w, http.StatusUnauthorized, "token_expired", "The temporary download token has expired")
					return
				}

				productID := r.PathValue("product")
				if productID != "" && dt.ProductID != productID {
					JSONError(w, http.StatusForbidden, "product_access_denied", "The download token is not authorized for this product")
					return
				}

				if dt.Version != "" {
					reqVersion := r.URL.Query().Get("version")
					if reqVersion != "" && reqVersion != dt.Version {
						JSONError(w, http.StatusForbidden, "version_access_denied", fmt.Sprintf("The download token is restricted to version %q", dt.Version))
						return
					}
				}

				// Increment download token usage asynchronously
				go func(tok string) {
					_ = database.IncrementDownloadTokenUsage(context.Background(), tok)
				}(token)

				ctx := context.WithValue(r.Context(), DownloadTokenContextKey, dt)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Otherwise, fall back to standard API key authentication with "download" scope
			RequireAPIKey(database, "download")(next).ServeHTTP(w, r)
		})
	}
}

// ConcurrencyLimiter gates in-flight downloads to prevent resource exhaustion.
type ConcurrencyLimiter struct {
	sem chan struct{}
}

func NewConcurrencyLimiter(limit int) *ConcurrencyLimiter {
	if limit <= 0 {
		limit = 50
	}
	return &ConcurrencyLimiter{
		sem: make(chan struct{}, limit),
	}
}

func (cl *ConcurrencyLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case cl.sem <- struct{}{}:
			defer func() { <-cl.sem }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "5")
			JSONError(w, http.StatusTooManyRequests, "too_many_requests", "Concurrent download limit reached. Please retry shortly.")
		}
	})
}

func extractToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// 2. X-API-Key: <token>
	if xKey := r.Header.Get("X-API-Key"); xKey != "" {
		return strings.TrimSpace(xKey)
	}

	// 3. ?api_key=<token>
	if qKey := r.URL.Query().Get("api_key"); qKey != "" {
		return strings.TrimSpace(qKey)
	}

	return ""
}

func hasScope(granted, required string) bool {
	if granted == "*" {
		return true
	}
	parts := strings.Split(granted, ",")
	for _, p := range parts {
		if strings.TrimSpace(p) == required {
			return true
		}
	}
	return false
}
