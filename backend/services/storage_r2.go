package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// R2Config holds R2 connection parameters.
type R2Config struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	PresignTTL      time.Duration
	// Endpoint override. Empty = derive from AccountID. Set in tests to point
	// at a local httptest server.
	Endpoint string
}

// R2Storage is the production Storage impl backed by Cloudflare R2 (S3 API).
type R2Storage struct {
	cfg       R2Config
	client    *s3.Client
	presigner *s3.PresignClient
}

// NewR2Storage creates an R2Storage from cfg. Returns an error only if the
// underlying AWS config cannot be loaded (extremely rare — static credentials
// never fail to load).
func NewR2Storage(cfg R2Config) (*R2Storage, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("r2: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &R2Storage{
		cfg:       cfg,
		client:    client,
		presigner: s3.NewPresignClient(client),
	}, nil
}

// Key returns an opaque forward-slash-joined cache key for (videoID, name).
// Pure function: no I/O, no error.
func (s *R2Storage) Key(videoID, name string) string { return path.Join(videoID, name) }

// Has reports whether the object at key exists in R2 with non-zero size.
// A missing object returns (false, nil). A zero-byte object also returns
// (false, nil) so that a corrupt or incomplete upload forces regeneration.
func (s *R2Storage) Has(ctx context.Context, key string) (bool, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &key,
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return false, nil
		}
		return false, fmt.Errorf("r2: HeadObject %s: %w", key, err)
	}
	return out.ContentLength != nil && *out.ContentLength > 0, nil
}

// SignGet returns a presigned GET URL valid for cfg.PresignTTL.
func (s *R2Storage) SignGet(ctx context.Context, key string) (string, error) {
	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &key,
	}, s3.WithPresignExpires(s.cfg.PresignTTL))
	if err != nil {
		return "", fmt.Errorf("r2: presign GET %s: %w", key, err)
	}
	return req.URL, nil
}

// SignPut returns a presigned PUT URL valid for cfg.PresignTTL.
// Used by the Python audio processor to upload pipeline outputs directly.
func (s *R2Storage) SignPut(ctx context.Context, key string) (string, error) {
	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &key,
	}, s3.WithPresignExpires(s.cfg.PresignTTL))
	if err != nil {
		return "", fmt.Errorf("r2: presign PUT %s: %w", key, err)
	}
	return req.URL, nil
}

// Commit uploads the file at localPath to R2 at key. Used for Go-side uploads
// (e.g. the yt-dlp audio download). Python uploads go through SignPut.
func (s *R2Storage) Commit(ctx context.Context, key, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("r2: open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &key,
		Body:   f,
	}); err != nil {
		return fmt.Errorf("r2: PutObject %s: %w", key, err)
	}
	return nil
}

// Open returns a ReadCloser streaming the object body from R2.
// Callers must close the returned reader.
func (s *R2Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.cfg.Bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("r2: GetObject %s: %w", key, err)
	}
	return out.Body, nil
}
