// Package mascotmedia stores anonymous contest images in a private S3 prefix.
package mascotmedia

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// FromEnv leaves storage disabled when no bucket has been configured.
func FromEnv() (*Store, error) {
	bucket := strings.TrimSpace(os.Getenv("MASCOT_S3_BUCKET"))
	if bucket == "" {
		return nil, nil
	}
	endpoint := os.Getenv("MASCOT_S3_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && os.Getenv("APP_ENV") == "development")) {
		return nil, errors.New("invalid mascot S3 endpoint")
	}
	prefix := strings.Trim(os.Getenv("MASCOT_S3_PREFIX"), "/")
	if prefix == "" || path.Clean(prefix) != prefix || strings.HasPrefix(prefix, "..") || prefix == "." {
		return nil, errors.New("invalid mascot S3 prefix")
	}
	region, accessKey, secret := os.Getenv("MASCOT_S3_REGION"), os.Getenv("MASCOT_S3_ACCESS_KEY_ID"), os.Getenv("MASCOT_S3_SECRET_ACCESS_KEY")
	if region == "" || accessKey == "" || secret == "" {
		return nil, errors.New("mascot S3 region and dedicated credentials are required")
	}
	cfg := aws.Config{
		Region: region, Credentials: credentials.NewStaticCredentialsProvider(accessKey, secret, ""),
		HTTPClient:                 &http.Client{Timeout: 30 * time.Second},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &Store{client: client, bucket: bucket, prefix: prefix}, nil
}

var opaqueKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (s *Store) objectKey(key string) (*string, error) {
	if !opaqueKey.MatchString(key) {
		return nil, errors.New("invalid mascot image key")
	}
	return aws.String(s.prefix + "/" + key), nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte, mime string) error {
	object, err := s.objectKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: object, Body: bytes.NewReader(data), ContentType: &mime,
	})
	return err
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	object, err := s.objectKey(key)
	if err != nil {
		return nil, "", err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: object})
	if err != nil {
		return nil, "", err
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return nil, "", errors.New("unable to read mascot image within size limit")
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return nil, "", errors.New("stored mascot object is not an image")
	}
	return data, mime, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	object, err := s.objectKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: object})
	return err
}
