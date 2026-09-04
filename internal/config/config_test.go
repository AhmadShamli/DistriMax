package config

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadDefaults(t *testing.T) {
	// Clear env
	_ = os.Unsetenv("HTTP_BIND_ADDRESS")
	_ = os.Unsetenv("STORAGE_BACKEND")
	_ = os.Unsetenv("SETTINGS_ENCRYPTION_KEY")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTPBindAddress != DefaultHTTPBindAddress {
		t.Errorf("expected %s, got %s", DefaultHTTPBindAddress, cfg.HTTPBindAddress)
	}
	if cfg.StorageBackend != "filesystem" {
		t.Errorf("expected filesystem, got %s", cfg.StorageBackend)
	}
}

func TestConfigEncryptionKey(t *testing.T) {
	// Valid 64-char hex key (32 bytes)
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_ = os.Setenv("SETTINGS_ENCRYPTION_KEY", hexKey)
	defer os.Unsetenv("SETTINGS_ENCRYPTION_KEY")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("expected valid key, got err: %v", err)
	}
	if len(cfg.SettingsEncryptionKey) != 32 {
		t.Errorf("expected 32 bytes key, got %d", len(cfg.SettingsEncryptionKey))
	}

	// Invalid short key
	_ = os.Setenv("SETTINGS_ENCRYPTION_KEY", "too-short")
	_, err = Load("")
	if err == nil {
		t.Error("expected error for short key, got nil")
	}
}

func TestTrustedProxies(t *testing.T) {
	_ = os.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.100")
	defer os.Unsetenv("TRUSTED_PROXIES")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.IsTrustedProxy(net.ParseIP("10.1.2.3")) {
		t.Error("expected 10.1.2.3 to be trusted")
	}
	if !cfg.IsTrustedProxy(net.ParseIP("192.168.1.100")) {
		t.Error("expected 192.168.1.100 to be trusted")
	}
	if cfg.IsTrustedProxy(net.ParseIP("8.8.8.8")) {
		t.Error("expected 8.8.8.8 to not be trusted")
	}
}

func TestEnsureDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		SQLitePath:   filepath.Join(tmpDir, "db", "distrimax.sqlite3"),
		ArtifactRoot: filepath.Join(tmpDir, "artifacts"),
		StagingRoot:  filepath.Join(tmpDir, "staging"),
		BackupRoot:   filepath.Join(tmpDir, "backups"),
	}

	if err := cfg.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories failed: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(cfg.SQLitePath)); err != nil {
		t.Errorf("sqlite dir not created: %v", err)
	}
	if _, err := os.Stat(cfg.ArtifactRoot); err != nil {
		t.Errorf("artifact root not created: %v", err)
	}
}
