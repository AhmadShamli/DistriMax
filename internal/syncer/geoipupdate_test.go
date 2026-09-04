package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGeoIPUpdateRunnerNew(t *testing.T) {
	// LookPath may succeed or fail depending on PATH
	_, _ = NewGeoIPUpdateRunner()

	runner := NewGeoIPUpdateRunnerWithPath("/nonexistent/geoipupdate")
	if runner == nil || runner.binaryPath != "/nonexistent/geoipupdate" {
		t.Fatalf("unexpected runner: %+v", runner)
	}

	ctx := context.Background()
	_, err := runner.Run(ctx, "account", "key", []string{"GeoLite2-City"}, t.TempDir())
	if err == nil {
		t.Error("expected error when executing nonexistent binary")
	}
}

func TestFindExtractedMMDB(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "GeoLite2-City.mmdb")

	// 1. Not found
	_, err := FindExtractedMMDB(tmpDir, "GeoLite2-City.mmdb")
	if err == nil {
		t.Error("expected error when file does not exist")
	}

	// 2. Found
	if err := os.WriteFile(targetFile, []byte("mmdb-content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	found, err := FindExtractedMMDB(tmpDir, "GeoLite2-City.mmdb")
	if err != nil {
		t.Fatalf("FindExtractedMMDB failed: %v", err)
	}
	if found != targetFile {
		t.Errorf("expected %s, got %s", targetFile, found)
	}
}
