package syncer

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var (
	ErrArtifactNotFoundInArchive = errors.New("expected mmdb artifact not found inside tar.gz archive")
	ErrArchiveTooLarge           = errors.New("extracted archive exceeded maximum allowed size")
	ErrPathTraversal             = errors.New("archive contains illegal path traversal entry")
)

const (
	MaxExtractedSizeBytes = 500 * 1024 * 1024 // 500 MB limit
)

// ExtractMMDBFromTarGz extracts the specific MMDB file from a compressed tar.gz stream
// and computes the SHA-256 hash of the extracted MMDB file.
func ExtractMMDBFromTarGz(r io.Reader, expectedFilename string) (data []byte, sha256Hex string, err error) {
	gzReader, err := gzip.NewReader(r)
	if err != nil {
		return nil, "", fmt.Errorf("failed to initialize gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	hasher := sha256.New()

	for {
		header, err := tarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, "", fmt.Errorf("tar reading error: %w", err)
		}

		// Prevent zip-slip
		cleanName := path.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") || strings.HasPrefix(cleanName, "/") {
			return nil, "", ErrPathTraversal
		}

		// MaxMind packs inside a timestamped folder, e.g. "GeoLite2-City_20260904/GeoLite2-City.mmdb"
		if header.Typeflag == tar.TypeReg && path.Base(cleanName) == expectedFilename {
			if header.Size > MaxExtractedSizeBytes {
				return nil, "", ErrArchiveTooLarge
			}

			// Read with limit
			limitedReader := io.LimitReader(tarReader, MaxExtractedSizeBytes+1)
			teedReader := io.TeeReader(limitedReader, hasher)

			extracted, err := io.ReadAll(teedReader)
			if err != nil {
				return nil, "", fmt.Errorf("failed to read extracted file: %w", err)
			}
			if int64(len(extracted)) > MaxExtractedSizeBytes {
				return nil, "", ErrArchiveTooLarge
			}

			return extracted, hex.EncodeToString(hasher.Sum(nil)), nil
		}
	}

	return nil, "", fmt.Errorf("%w: %s", ErrArtifactNotFoundInArchive, expectedFilename)
}
