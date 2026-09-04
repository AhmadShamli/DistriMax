package syncer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/AhmadShamli/DistriMax/internal/db"
	"github.com/AhmadShamli/DistriMax/internal/storage"
)

func encodeString(s string) []byte {
	return append([]byte{byte(2<<5 | len(s))}, []byte(s)...)
}

func encodeUint(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	valBytes := b[i:]
	if len(valBytes) == 0 {
		valBytes = []byte{0}
	}
	ctrl := byte(6<<5 | len(valBytes))
	return append([]byte{ctrl}, valBytes...)
}

func buildMockMMDB(dbType string, epoch time.Time) []byte {
	var meta bytes.Buffer
	meta.WriteByte(byte(7<<5 | 7)) // 7 entries in metadata map

	meta.Write(encodeString("binary_format_major_version"))
	meta.Write(encodeUint(2))

	meta.Write(encodeString("binary_format_minor_version"))
	meta.Write(encodeUint(0))

	meta.Write(encodeString("build_epoch"))
	meta.Write(encodeUint(uint64(epoch.Unix())))

	meta.Write(encodeString("database_type"))
	meta.Write(encodeString(dbType))

	meta.Write(encodeString("ip_version"))
	meta.Write(encodeUint(6))

	meta.Write(encodeString("record_size"))
	meta.Write(encodeUint(24))

	meta.Write(encodeString("node_count"))
	meta.Write(encodeUint(0))

	var buf bytes.Buffer
	buf.Write(make([]byte, 16)) // 16 bytes data separator
	buf.Write([]byte("\xAB\xCD\xEFMaxMind.com"))
	buf.Write(meta.Bytes())

	return buf.Bytes()
}

func createTarGz(filename string, fileContent []byte) []byte {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzWriter)

	hdr := &tar.Header{
		Name:     "GeoLite2-City_20260904/" + filename,
		Mode:     0644,
		Size:     int64(len(fileContent)),
		Typeflag: tar.TypeReg,
	}
	_ = tarWriter.WriteHeader(hdr)
	_, _ = tarWriter.Write(fileContent)
	_ = tarWriter.Close()
	_ = gzWriter.Close()

	return buf.Bytes()
}

func TestValidateMMDB(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	data := buildMockMMDB("GeoLite2-City", now)

	meta, err := ValidateMMDB(data, "GeoLite2-City")
	if err != nil {
		t.Fatalf("ValidateMMDB failed: %v", err)
	}

	if meta.DatabaseType != "GeoLite2-City" {
		t.Errorf("expected GeoLite2-City, got %s", meta.DatabaseType)
	}
	if meta.BuildEpoch.Unix() != now.Unix() {
		t.Errorf("expected epoch %d, got %d", now.Unix(), meta.BuildEpoch.Unix())
	}

	// Test mismatch
	_, err = ValidateMMDB(data, "GeoLite2-Country")
	if err == nil {
		t.Error("expected error for mismatched database type")
	}

	// Test corrupted header
	_, err = ValidateMMDB([]byte("corrupted data that is not mmdb"), "")
	if err == nil {
		t.Error("expected error for corrupted mmdb")
	}
}

func TestExtractMMDBFromTarGz(t *testing.T) {
	mmdbBytes := buildMockMMDB("GeoLite2-City", time.Now())
	tarGzBytes := createTarGz("GeoLite2-City.mmdb", mmdbBytes)

	extracted, sha, err := ExtractMMDBFromTarGz(bytes.NewReader(tarGzBytes), "GeoLite2-City.mmdb")
	if err != nil {
		t.Fatalf("ExtractMMDBFromTarGz failed: %v", err)
	}

	if !bytes.Equal(extracted, mmdbBytes) {
		t.Error("extracted content did not match original MMDB bytes")
	}
	if sha == "" {
		t.Error("expected computed sha256, got empty string")
	}

	// Test missing file
	_, _, err = ExtractMMDBFromTarGz(bytes.NewReader(tarGzBytes), "Wrong-Name.mmdb")
	if err == nil {
		t.Error("expected error for missing file inside archive")
	}
}

func TestFullSyncOrchestration(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. Setup DB
	database, err := db.Open(filepath.Join(tmpDir, "sync_test.sqlite3"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()
	_ = db.RunMigrations(ctx, database.DB)

	// Configure credentials in settings
	_ = database.SetSetting(ctx, "maxmind_account_id", "test_account", false, "test")
	_ = database.SetSetting(ctx, "maxmind_license_key", "test_key", false, "test")

	// 2. Setup Storage
	store, err := storage.NewFilesystemStorage(filepath.Join(tmpDir, "artifacts"), filepath.Join(tmpDir, "staging"))
	if err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	// 3. Setup Mock Upstream MaxMind Server
	epoch := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	mmdbData := buildMockMMDB("GeoLite2-City", epoch)
	tarGzData := createTarGz("GeoLite2-City.mmdb", mmdbData)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Check Basic Auth
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test_account" || pass != "test_key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check If-Modified-Since
		ims := r.Header.Get("If-Modified-Since")
		if ims != "" {
			parsed, err := http.ParseTime(ims)
			if err == nil && !parsed.Before(epoch) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		w.Header().Set("Last-Modified", epoch.Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(tarGzData)
	}))
	defer server.Close()

	// 4. Create Syncer
	maxmindClient := NewMaxMindClient(server.URL, 5*time.Second)
	publishedCalled := false
	syncer := NewSyncer(database, store, maxmindClient, nil, func(pv *db.ProductVersion) {
		publishedCalled = true
	})

	// 5. Execute First Sync (should succeed with new version)
	res, err := syncer.SyncProduct(ctx, "geolite-city", "MANUAL")
	if err != nil {
		t.Fatalf("SyncProduct failed: %v", err)
	}

	if res.Status != "SUCCESS" {
		t.Errorf("expected SUCCESS, got %s", res.Status)
	}
	if res.Version != "2026-09-04" {
		t.Errorf("expected version 2026-09-04, got %s", res.Version)
	}
	if !publishedCalled {
		t.Error("expected onPublish callback to be invoked")
	}

	// Verify current version in DB
	cur, err := database.GetCurrentVersion(ctx, "geolite-city")
	if err != nil {
		t.Fatalf("GetCurrentVersion failed: %v", err)
	}
	if cur.Version != "2026-09-04" || cur.SHA256 != res.SHA256 {
		t.Errorf("db record mismatch: %+v", cur)
	}

	// 6. Execute Second Sync (should receive 304 Not Modified and be SKIPPED)
	res2, err := syncer.SyncProduct(ctx, "geolite-city", "SCHEDULED")
	if err != nil {
		t.Fatalf("Second SyncProduct failed: %v", err)
	}
	if res2.Status != "SKIPPED" {
		t.Errorf("expected second sync to be SKIPPED, got %s", res2.Status)
	}
}
