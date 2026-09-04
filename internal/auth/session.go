package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// GenerateSessionToken creates a cryptographically secure session ID.
func GenerateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes for session: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateCSRFToken creates a cryptographically secure CSRF token.
func GenerateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read random bytes for csrf: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateCSRFToken checks if the provided CSRF token matches the session CSRF token in constant time.
func ValidateCSRFToken(expected, provided string) bool {
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
