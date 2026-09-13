package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3Backend stocke les images dans un service de stockage objet compatible S3.
// Fonctionne avec AWS S3, MinIO, DigitalOcean Spaces, etc.
type S3Backend struct {
	client *s3.Client
	bucket string
}

// NewS3Backend crée un nouveau backend S3 avec les credentials fournis.
func NewS3Backend(endpoint, region, bucket, accessKey, secretKey string, useSSL, forcePathStyle bool) (*S3Backend, error) {
	scheme := "https"
	if !useSSL {
		scheme = "http"
	}
	fullEndpoint := fmt.Sprintf("%s://%s", scheme, endpoint)

	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion(region),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(fullEndpoint)
		o.UsePathStyle = forcePathStyle
	})

	return &S3Backend{client: client, bucket: bucket}, nil
}

func (b *S3Backend) Type() string { return "s3" }

func (b *S3Backend) Put(ctx context.Context, path string, reader io.Reader, size int64) (int64, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
		Body:   reader,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if _, err := b.client.PutObject(ctx, input); err != nil {
		return 0, fmt.Errorf("s3 put: %w", err)
	}
	return size, nil
}

func (b *S3Backend) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get: %w", err)
	}
	return out.Body, nil
}

func (b *S3Backend) GetRange(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
	rng := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
		Range:  aws.String(rng),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get range: %w", err)
	}
	return out.Body, nil
}

func (b *S3Backend) Delete(ctx context.Context, path string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	return err
}

func (b *S3Backend) Exists(ctx context.Context, path string) (bool, error) {
	_, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return false, nil
		}
		return false, fmt.Errorf("s3 head: %w", err)
	}
	return true, nil
}

func (b *S3Backend) Size(ctx context.Context, path string) (int64, error) {
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return 0, err
	}
	if out.ContentLength != nil {
		return *out.ContentLength, nil
	}
	return 0, nil
}
