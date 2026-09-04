package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GeoIPUpdateRunner wraps the external geoipupdate CLI binary if installed.
type GeoIPUpdateRunner struct {
	binaryPath string
}

func NewGeoIPUpdateRunner() (*GeoIPUpdateRunner, error) {
	path, err := exec.LookPath("geoipupdate")
	if err != nil {
		return nil, fmt.Errorf("geoipupdate binary not found in PATH: %w", err)
	}
	return &GeoIPUpdateRunner{binaryPath: path}, nil
}

// NewGeoIPUpdateRunnerWithPath creates a runner using a specific binary location.
func NewGeoIPUpdateRunnerWithPath(binaryPath string) *GeoIPUpdateRunner {
	return &GeoIPUpdateRunner{binaryPath: binaryPath}
}

// Run executes the geoipupdate tool to download the specified edition IDs to targetDir.
func (g *GeoIPUpdateRunner) Run(ctx context.Context, accountID, licenseKey string, editionIDs []string, targetDir string) (string, error) {
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create target directory: %w", err)
	}

	// Create temporary GeoIP.conf
	confContent := fmt.Sprintf("AccountID %s\nLicenseKey %s\nEditionIDs %s\nDatabaseDirectory %s\n",
		accountID, licenseKey, strings.Join(editionIDs, " "), targetDir)

	confFile, err := os.CreateTemp("", "GeoIP-*.conf")
	if err != nil {
		return "", fmt.Errorf("failed to create temp config: %w", err)
	}
	defer func() {
		_ = os.Remove(confFile.Name())
	}()

	if _, err := confFile.WriteString(confContent); err != nil {
		_ = confFile.Close()
		return "", fmt.Errorf("failed to write temp config: %w", err)
	}
	_ = confFile.Close()

	cmd := exec.CommandContext(ctx, g.binaryPath, "-f", confFile.Name(), "-d", targetDir, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("geoipupdate execution failed: %w (output: %s)", err, string(out))
	}

	return string(out), nil
}

// FindExtractedMMDB finds a specific MMDB file in the directory after geoipupdate finishes.
func FindExtractedMMDB(dir, filename string) (string, error) {
	target := filepath.Join(dir, filename)
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return target, nil
	}
	return "", fmt.Errorf("file %s not found in %s", filename, dir)
}
