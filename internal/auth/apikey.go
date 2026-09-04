package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	APIKeyLivePrefix = "dm_live_"
	PrefixLength     = 16 // "dm_live_" (8) + 8 hex chars = 16 chars
)

// GenerateAPIKey generates a high-entropy API key, its public prefix, and its Argon2id hash.
func GenerateAPIKey() (fullSecret, prefix, hash string, err error) {
	// Generate 24 random bytes (48 hex characters)
	randomBytes := make([]byte, 24)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	hexSecret := hex.EncodeToString(randomBytes)
	fullSecret = APIKeyLivePrefix + hexSecret

	// Prefix is first 16 characters for fast indexing/display in UI
	prefix = fullSecret[:PrefixLength]

	// Compute Argon2id hash of full secret
	hash, err = HashPassword(fullSecret)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to hash api key: %w", err)
	}

	return fullSecret, prefix, hash, nil
}

// ExtractPrefix returns the prefix from a full API key token string.
func ExtractPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) >= PrefixLength {
		return token[:PrefixLength]
	}
	return token
}
