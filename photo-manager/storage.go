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
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/encoding/httpbinding"
)

var errConflict = errors.New("the library changed; reload before saving again")

type object struct {
	Body []byte
	ETag string
}

type storage interface {
	List(context.Context, string, string) ([]string, error)
	Read(context.Context, string, string) (*object, error)
	Write(context.Context, string, string, []byte, string, string, *string) error
	Copy(context.Context, string, string, string, string, string) error
	Delete(context.Context, string, string) error
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

// Copy keeps derivative bytes within R2 and replaces private metadata with the
// destination's content type and cache policy. The SDK sends CopySource verbatim.
func (s *r2Storage) Copy(ctx context.Context, sourceBucket, destinationBucket, key, contentType, cache string) error {
	source := httpbinding.EscapePath(s.buckets[sourceBucket]+"/"+key, false)
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(s.buckets[destinationBucket]), Key: &key, CopySource: &source,
		MetadataDirective: types.MetadataDirectiveReplace,
		ContentType:       &contentType, CacheControl: &cache,
	})
	return err
}

func (s *r2Storage) Delete(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.buckets[bucket]), Key: &key})
	return err
}

func (s *r2Storage) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	keys := []string{}
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.buckets[bucket]), Prefix: &prefix})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Contents {
			keys = append(keys, aws.ToString(item.Key))
		}
	}
	return keys, nil
}

// Development-only adapter. Production has no disk database or persistent volume.
type localStorage struct {
	root string
	mu   sync.Mutex
}

func (s *localStorage) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	if bucket != "private" && bucket != "public" && bucket != "protected" {
		return nil, errors.New("invalid storage bucket")
	}
	keys := []string{}
	root := filepath.Join(s.root, bucket)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			key, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			key = filepath.ToSlash(key)
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
		return ctx.Err()
	})
	return keys, err
}

func (s *localStorage) filename(bucket, key string) (string, error) {
	if (bucket != "private" && bucket != "public" && bucket != "protected") || !fs.ValidPath(key) {
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

func (s *localStorage) Copy(ctx context.Context, sourceBucket, destinationBucket, key, contentType, cache string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := s.Read(ctx, sourceBucket, key)
	if err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("copy source %s/%s: %w", sourceBucket, key, os.ErrNotExist)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Write(ctx, destinationBucket, key, source.Body, contentType, cache, nil)
}

func (s *localStorage) Delete(ctx context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filename, err := s.filename(bucket, key)
	if err != nil {
		return err
	}
	if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
