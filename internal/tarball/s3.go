package tarball

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/vibed-project/vibeD/internal/config"
)

// s3Store streams tarballs to an S3-compatible bucket (AWS S3 or MinIO) and
// returns pre-signed GET URLs. Pre-signing keeps the control plane stateless:
// the agent fetches straight from object storage, not through vibeD.
type s3Store struct {
	client     *s3.Client
	presign    *s3.PresignClient
	bucket     string
	presignTTL time.Duration
}

func newS3Store(cfg config.S3TarballConfig) (*s3Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("tarball s3: storage.tarball.s3.bucket is required")
	}
	ttl := 15 * time.Minute
	if cfg.PresignTTL != "" {
		d, err := time.ParseDuration(cfg.PresignTTL)
		if err != nil {
			return nil, fmt.Errorf("tarball s3: invalid presignTTL %q: %w", cfg.PresignTTL, err)
		}
		ttl = d
	}

	awsCfg := aws.Config{Region: cfg.Region}
	if cfg.AccessKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			// MinIO / non-AWS: explicit endpoint + path-style addressing
			// (MinIO doesn't do virtual-hosted-style buckets by default).
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})

	return &s3Store{
		client:     client,
		presign:    s3.NewPresignClient(client),
		bucket:     cfg.Bucket,
		presignTTL: ttl,
	}, nil
}

func (s *s3Store) Put(ctx context.Context, id string, r io.Reader) (string, error) {
	if id == "" {
		return "", fmt.Errorf("tarball s3: id is empty")
	}
	key := objectName(id)
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        r,
		ContentType: aws.String("application/gzip"),
	}); err != nil {
		return "", fmt.Errorf("tarball s3: put %s: %w", key, err)
	}

	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(s.presignTTL))
	if err != nil {
		return "", fmt.Errorf("tarball s3: presign %s: %w", key, err)
	}
	return req.URL, nil
}

func (s *s3Store) Delete(ctx context.Context, id string) error {
	key := objectName(id)
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("tarball s3: delete %s: %w", key, err)
	}
	return nil
}
