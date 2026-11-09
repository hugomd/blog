package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var errConflict = errors.New("the library changed; reload before saving again")

type object struct {
	Body []byte
	ETag string
}

type storage interface {
	Read(context.Context, string, string) (*object, error)
	Write(context.Context, string, string, []byte, string, string, *string) error
}

type r2Storage struct {
	client  *s3.Client
	buckets map[string]string
}

func newR2Storage(endpoint, key, secret, privateBucket, publicBucket string) *r2Storage {
	client := s3.NewFromConfig(aws.Config{
		Region: "auto", Credentials: credentials.NewStaticCredentialsProvider(key, secret, ""),
		HTTPClient: &http.Client{Timeout: 45 * time.Second}, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(o *s3.Options) { o.BaseEndpoint = &endpoint; o.UsePathStyle = true })
	return &r2Storage{client: client, buckets: map[string]string{"private": privateBucket, "public": publicBucket}}
}

func (s *r2Storage) Read(ctx context.Context, bucket, key string) (*object, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.buckets[bucket]), Key: &key})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, nil
		}
		return nil, err
	}
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}
	return &object{Body: body, ETag: aws.ToString(result.ETag)}, nil
}

// expected=nil is unconditional; expected="" creates only; otherwise match the ETag.
func (s *r2Storage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	input := &s3.PutObjectInput{Bucket: aws.String(s.buckets[bucket]), Key: &key, Body: bytes.NewReader(body), ContentType: &contentType, CacheControl: &cache}
	if expected != nil {
		if *expected == "" {
			input.IfNoneMatch = aws.String("*")
		} else {
			input.IfMatch = expected
		}
	}
	_, err := s.client.PutObject(ctx, input)
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict") {
		return errConflict
	}
	return err
}

// Development-only adapter. Production has no disk database or persistent volume.
type localStorage struct {
	root string
	mu   sync.Mutex
}

func (s *localStorage) filename(bucket, key string) (string, error) {
	if (bucket != "private" && bucket != "public") || !fs.ValidPath(key) {
		return "", errors.New("invalid storage key")
	}
	return filepath.Join(s.root, bucket, filepath.FromSlash(key)), nil
}

func (s *localStorage) Read(ctx context.Context, bucket, key string) (*object, error) {
	filename, err := s.filename(bucket, key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &object{Body: body, ETag: fmt.Sprintf("\"%x\"", sha256.Sum256(body))}, nil
}

func (s *localStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != nil {
		current, err := s.Read(ctx, bucket, key)
		if err != nil {
			return err
		}
		etag := ""
		if current != nil {
			etag = current.ETag
		}
		if etag != *expected {
			return errConflict
		}
	}
	filename, err := s.filename(bucket, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filename)
}
