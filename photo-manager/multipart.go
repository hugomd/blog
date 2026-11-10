package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const uploadPartSize int64 = 8 << 20

type uploadPart struct {
	Number int32
	Size   int64
	ETag   string
}

type multipartStorage interface {
	BeginUpload(context.Context, string) (string, error)
	PartURL(context.Context, string, string, int32, int64) (string, error)
	ListUploadParts(context.Context, string, string) ([]uploadPart, error)
	FinishUpload(context.Context, string, string, []uploadPart) error
	AbortUpload(context.Context, string, string) error
}

func (s *r2Storage) BeginUpload(ctx context.Context, key string) (string, error) {
	result, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(s.buckets["private"]), Key: &key, ContentType: aws.String("application/octet-stream"), CacheControl: aws.String("private, no-store")})
	if err != nil {
		return "", err
	}
	return aws.ToString(result.UploadId), nil
}

func (s *r2Storage) PartURL(ctx context.Context, key, id string, number int32, size int64) (string, error) {
	request, err := s3.NewPresignClient(s.client).PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(s.buckets["private"]), Key: &key, UploadId: &id, PartNumber: &number, ContentLength: &size}, func(o *s3.PresignOptions) { o.Expires = time.Hour })
	if err != nil {
		return "", err
	}
	return request.URL, nil
}

func (s *r2Storage) ListUploadParts(ctx context.Context, key, id string) ([]uploadPart, error) {
	parts := []uploadPart{}
	pages := s3.NewListPartsPaginator(s.client, &s3.ListPartsInput{Bucket: aws.String(s.buckets["private"]), Key: &key, UploadId: &id})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, part := range page.Parts {
			parts = append(parts, uploadPart{aws.ToInt32(part.PartNumber), aws.ToInt64(part.Size), aws.ToString(part.ETag)})
		}
	}
	return parts, nil
}

func (s *r2Storage) FinishUpload(ctx context.Context, key, id string, parts []uploadPart) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completed[i] = types.CompletedPart{ETag: aws.String(part.ETag), PartNumber: aws.Int32(part.Number)}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(s.buckets["private"]), Key: &key, UploadId: &id, MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
	return err
}

func (s *r2Storage) AbortUpload(ctx context.Context, key, id string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.buckets["private"]), Key: &key, UploadId: &id})
	var apiError smithy.APIError
	if errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchUpload" {
		return nil
	}
	return err
}

// Development uses authenticated same-origin chunk endpoints. Production never
// serves those endpoints: its signed URLs send file bytes straight to R2.
func (s *localStorage) BeginUpload(ctx context.Context, key string) (string, error) {
	id := identifier()
	path, err := s.filename("private", "multipart/"+id)
	if err != nil {
		return "", err
	}
	return id, os.MkdirAll(path, 0700)
}

func (s *localStorage) PartURL(ctx context.Context, key, id string, number int32, size int64) (string, error) {
	return fmt.Sprintf("/api/uploads/%s/parts/%d", strings.TrimPrefix(key, "originals/"), number), nil
}

func (s *localStorage) ListUploadParts(ctx context.Context, key, id string) ([]uploadPart, error) {
	keys, err := s.List(ctx, "private", "multipart/"+id+"/")
	if err != nil {
		return nil, err
	}
	parts := []uploadPart{}
	for _, key := range keys {
		number, err := strconv.ParseInt(filepath.Base(key), 10, 32)
		if err != nil {
			return nil, err
		}
		value, err := s.Read(ctx, "private", key)
		if err != nil {
			return nil, err
		}
		parts = append(parts, uploadPart{int32(number), int64(len(value.Body)), value.ETag})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	return parts, nil
}

func (s *localStorage) FinishUpload(ctx context.Context, key, id string, parts []uploadPart) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	destination, err := s.filename("private", key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".multipart-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	for _, part := range parts {
		value, err := s.Read(ctx, "private", fmt.Sprintf("multipart/%s/%d", id, part.Number))
		if err != nil {
			return err
		}
		if value == nil || value.ETag != part.ETag {
			return errConflict
		}
		if _, err := tmp.Write(value.Body); err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), destination)
}

func (s *localStorage) AbortUpload(ctx context.Context, key, id string) error {
	keys, err := s.List(ctx, "private", "multipart/"+id+"/")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.Delete(ctx, "private", key); err != nil {
			return err
		}
	}
	return nil
}
