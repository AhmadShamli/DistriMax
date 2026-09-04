package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config defines connection parameters for an S3-compatible backend.
type S3Config struct {
	Endpoint        string
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// S3Storage implements StorageBackend for S3, MinIO, and Cloudflare R2.
type S3Storage struct {
	client *s3.Client
	bucket string
}

// NewS3Storage creates an initialized S3Storage backend.
func NewS3Storage(cfg S3Config) (*S3Storage, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3 bucket cannot be empty")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if cfg.Endpoint != "" {
			return aws.Endpoint{
				URL:               cfg.Endpoint,
				SigningRegion:     cfg.Region,
				HostnameImmutable: true,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	awsCfg := aws.Config{
		Region:                      cfg.Region,
		Credentials:                 credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		EndpointResolverWithOptions: customResolver,
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &S3Storage{
		client: client,
		bucket: cfg.Bucket,
	}, nil
}

// StoreArtifact streams into S3 and verifies size.
func (s *S3Storage) StoreArtifact(ctx context.Context, product, version, filename string, r io.Reader, size int64) (string, error) {
	if err := validateIdentifiers(product, version, filename); err != nil {
		return "", err
	}

	objectKey := path.Join("artifacts", product, version, filename)

	// Stream upload to S3
	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          r,
		ContentLength: aws.Int64(size),
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("s3 put object failed: %w", err)
	}

	return objectKey, nil
}

// OpenArtifact retrieves the object. For Seek support across HTTP Range requests,
// it wraps range-aware reads or buffers into a seekable stream.
func (s *S3Storage) OpenArtifact(ctx context.Context, storagePath string) (io.ReadSeekCloser, int64, error) {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}

	size := aws.ToInt64(head.ContentLength)

	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return nil, 0, err
	}

	// Buffer into memory/bytes.Reader for Seek support in v1
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read s3 object body: %w", err)
	}

	return &seekableBuffer{Reader: bytes.NewReader(data)}, size, nil
}

// DeleteArtifact removes the object from the S3 bucket.
func (s *S3Storage) DeleteArtifact(ctx context.Context, storagePath string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object failed: %w", err)
	}
	return nil
}

// VerifyArtifact validates that the object exists and matches the expected size.
func (s *S3Storage) VerifyArtifact(ctx context.Context, storagePath string, expectedSize int64) error {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return ErrNotFound
		}
		return err
	}

	actualSize := aws.ToInt64(head.ContentLength)
	if expectedSize > 0 && actualSize != expectedSize {
		return fmt.Errorf("%w: expected %d bytes, got %d bytes", ErrSizeMismatch, expectedSize, actualSize)
	}

	return nil
}

type seekableBuffer struct {
	*bytes.Reader
}

func (s *seekableBuffer) Close() error {
	return nil
}

func (s *S3Storage) resolveSafePath(storagePath string) (string, error) {
	clean := path.Clean(storagePath)
	if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
		return "", ErrInvalidPath
	}
	return clean, nil
}
