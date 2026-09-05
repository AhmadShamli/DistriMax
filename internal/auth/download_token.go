package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	DownloadTokenPrefix = "dmt_"
)

// GenerateDownloadToken creates a cryptographically secure random token for temporary direct downloads.
func GenerateDownloadToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes for download token: %w", err)
	}
	return DownloadTokenPrefix + hex.EncodeToString(b), nil
}
