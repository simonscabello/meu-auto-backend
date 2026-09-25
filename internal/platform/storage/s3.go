package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config is what an S3-compatible bucket needs. Railway injects these as ENDPOINT,
// BUCKET, ACCESS_KEY_ID, SECRET_ACCESS_KEY and REGION on the bucket, referenced from the
// service's BUCKET_* variables.
type S3Config struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	Region          string

	// PathStyle addresses the bucket as endpoint/bucket rather than bucket.endpoint.
	// Railway Buckets created before late 2025 need it; newer ones do not.
	PathStyle bool
}

// S3 is a Store backed by an S3-compatible bucket.
type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// NewS3 builds the client. It does not reach the network: a wrong credential surfaces
// on the first Put, not at boot, so a bucket outage cannot stop the API from starting.
func NewS3(cfg S3Config) *S3 {
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.PathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// Tigris and other S3-compatibles reject the newer default CRC checksums on
		// PutObject; ask for them only where the operation requires one.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket}
}

// Ping asks the bucket whether it exists and the credentials open it. Boot calls it to
// log the answer, never to refuse to start: a bucket outage costs photos, not the API.
func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("head bucket: %w", err)
	}
	return nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
		// A key is written once and never rewritten (a new photo is a new key), so a
		// client may keep what it downloaded for as long as the signed URL lets it.
		CacheControl: aws.String("private, max-age=86400, immutable"),
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	var missing *types.NoSuchKey
	if err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (s *S3) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return req.URL, nil
}
