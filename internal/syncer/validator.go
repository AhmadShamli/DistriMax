package syncer

import (
	"errors"
	"fmt"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

var (
	ErrInvalidMMDBHeader   = errors.New("invalid mmdb database header")
	ErrDatabaseTypeMismatch = errors.New("database type mismatch")
)

type MMDBMetadata struct {
	DatabaseType string
	RecordSize   uint
	BuildEpoch   time.Time
	NodeCount    uint
	IPVersion    uint
}

// ValidateMMDB inspects the extracted bytes to ensure it is a valid MaxMind database.
func ValidateMMDB(data []byte, expectedType string) (*MMDBMetadata, error) {
	if len(data) < 16 {
		return nil, ErrInvalidMMDBHeader
	}

	reader, err := maxminddb.FromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMMDBHeader, err)
	}
	defer reader.Close()

	meta := reader.Metadata
	if meta.DatabaseType == "" {
		return nil, ErrInvalidMMDBHeader
	}

	if expectedType != "" && meta.DatabaseType != expectedType {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrDatabaseTypeMismatch, expectedType, meta.DatabaseType)
	}

	return &MMDBMetadata{
		DatabaseType: meta.DatabaseType,
		RecordSize:   meta.RecordSize,
		BuildEpoch:   time.Unix(int64(meta.BuildEpoch), 0).UTC(),
		NodeCount:    meta.NodeCount,
		IPVersion:    meta.IPVersion,
	}, nil
}
