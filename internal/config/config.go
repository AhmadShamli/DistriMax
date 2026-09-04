package config

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Config holds all startup and infrastructure configuration for DistriMax.
type Config struct {
	HTTPBindAddress         string
	SQLitePath              string
	ArtifactRoot            string
	StagingRoot             string
	BackupRoot              string
	BootstrapSecret         string
	RecoverySecret          string
	SettingsEncryptionKey   []byte // 32-byte key for AES-256-GCM
	StorageBackend          string // "filesystem" or "s3"
	TrustedProxies          []*net.IPNet
	TrustedProxiesRaw       string
}

// Default constants
const (
	DefaultHTTPBindAddress = ":8080"
	DefaultSQLitePath      = "/var/lib/distrimax/distrimax.sqlite3"
	DefaultArtifactRoot    = "/var/lib/distrimax/artifacts"
	DefaultStagingRoot     = "/var/lib/distrimax/staging"
	DefaultBackupRoot      = "/var/lib/distrimax/backups"
	DefaultStorageBackend  = "filesystem"
)

// Load reads configuration from the environment and an optional .env file.
func Load(envFile string) (*Config, error) {
	if envFile != "" {
		_ = loadDotEnv(envFile)
	} else if _, err := os.Stat(".env"); err == nil {
		_ = loadDotEnv(".env")
	}

	cfg := &Config{
		HTTPBindAddress:   getEnv("HTTP_BIND_ADDRESS", DefaultHTTPBindAddress),
		SQLitePath:        getEnv("SQLITE_PATH", DefaultSQLitePath),
		ArtifactRoot:      getEnv("ARTIFACT_ROOT", DefaultArtifactRoot),
		StagingRoot:       getEnv("STAGING_ROOT", DefaultStagingRoot),
		BackupRoot:        getEnv("BACKUP_ROOT", DefaultBackupRoot),
		BootstrapSecret:   getEnv("DISTRIMAX_BOOTSTRAP_SECRET", ""),
		RecoverySecret:    getEnv("DISTRIMAX_RECOVERY_SECRET", ""),
		StorageBackend:    strings.ToLower(getEnv("STORAGE_BACKEND", DefaultStorageBackend)),
		TrustedProxiesRaw: getEnv("TRUSTED_PROXIES", "127.0.0.1/32"),
	}

	// Parse settings encryption key
	rawKey := os.Getenv("SETTINGS_ENCRYPTION_KEY")
	if rawKey != "" {
		key, err := parseEncryptionKey(rawKey)
		if err != nil {
			return nil, fmt.Errorf("invalid SETTINGS_ENCRYPTION_KEY: %w", err)
		}
		cfg.SettingsEncryptionKey = key
	}

	// Parse trusted proxy CIDRs
	if cfg.TrustedProxiesRaw != "" {
		subnets, err := parseCIDRList(cfg.TrustedProxiesRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
		}
		cfg.TrustedProxies = subnets
	}

	// Validate required paths and backend
	if cfg.StorageBackend != "filesystem" && cfg.StorageBackend != "s3" {
		return nil, fmt.Errorf("unsupported STORAGE_BACKEND: %s (must be 'filesystem' or 's3')", cfg.StorageBackend)
	}

	return cfg, nil
}

// EnsureDirectories verifies and creates necessary storage directories for filesystem backend.
func (c *Config) EnsureDirectories() error {
	dirs := []string{
		filepath.Dir(c.SQLitePath),
		c.ArtifactRoot,
		c.StagingRoot,
		c.BackupRoot,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// IsTrustedProxy checks whether a given IP matches any configured trusted proxy CIDR.
func (c *Config) IsTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range c.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func parseEncryptionKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	// If hex-encoded (64 characters = 32 bytes)
	if len(raw) == 64 {
		decoded, err := hex.DecodeString(raw)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	// If exactly 32 bytes plain text
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, errors.New("key must be exactly 32 bytes (or 64 hex characters)")
}

func parseCIDRList(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	var result []*net.IPNet
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128" // IPv6 single host
			} else {
				p += "/32" // IPv4 single host
			}
		}
		_, network, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", p, err)
		}
		result = append(result, network)
	}
	return result, nil
}

// loadDotEnv parses key=val lines from a file without overriding existing environment variables.
func loadDotEnv(filepath string) error {
	f, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			if _, exists := os.LookupEnv(key); !exists {
				_ = os.Setenv(key, val)
			}
		}
	}
	return scanner.Err()
}
