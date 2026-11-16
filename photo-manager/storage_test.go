package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/smithy-go"
)

// Exercise the real AWS SDK over HTTP against an S3 conditional-write fixture.
// This does not substitute for a live R2 credential/permissions acceptance test.
func TestR2ManifestListingFollowsPaginationAndPropagatesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path != "/gallery" || r.URL.Query().Get("list-type") != "2" {
			t.Errorf("Wrong listing request: %s", r.URL)
		}
		if r.URL.Query().Get("prefix") == "forbidden/" {
			w.WriteHeader(403)
			fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
			return
		}
		if r.URL.Query().Get("prefix") != "releases/" {
			t.Errorf("Unscoped listing: %s", r.URL)
		}
		switch r.URL.Query().Get("continuation-token") {
		case "":
			fmt.Fprint(w, "<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next-page</NextContinuationToken><Contents><Key>releases/first.json</Key></Contents></ListBucketResult>")
		case "next-page":
			fmt.Fprint(w, "<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>releases/last.json</Key></Contents></ListBucketResult>")
		default:
			t.Errorf("Unexpected pagination token: %s", r.URL)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	store := newR2Storage(server.URL, "test-key", "test-secret", "originals", "gallery")
	keys, err := store.List(t.Context(), "public", "releases/")
	if err != nil || strings.Join(keys, ",") != "releases/first.json,releases/last.json" {
		t.Fatalf("Incomplete listing: %v %v", keys, err)
	}
	if _, err := store.List(t.Context(), "public", "forbidden/"); err == nil {
		t.Fatal("Permission failure ignored")
	}
}

func TestR2ConditionalCatalogWrites(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]object{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		value, exists := objects[r.URL.Path]
		s3Error := func(status int, code string) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			fmt.Fprintf(w, "<Error><Code>%s</Code><Message>test fixture</Message></Error>", code)
		}
		if r.Method == "GET" {
			if !exists {
				s3Error(404, "NoSuchKey")
				return
			}
			w.Header().Set("ETag", value.ETag)
			w.Write(value.Body)
			return
		}
		if r.Method == "DELETE" {
			if strings.HasSuffix(r.URL.Path, "/forbidden") {
				s3Error(403, "AccessDenied")
				return
			}
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
			return
		}
		if r.Method != "PUT" {
			w.WriteHeader(405)
			return
		}
		if (r.Header.Get("If-None-Match") == "*" && exists) || (r.Header.Get("If-Match") != "" && (!exists || value.ETag != r.Header.Get("If-Match"))) {
			s3Error(412, "PreconditionFailed")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			s3Error(500, "InternalError")
			return
		}
		etag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		objects[r.URL.Path] = object{Body: body, ETag: etag}
		w.Header().Set("ETag", etag)
	}))
	defer server.Close()
	store := newR2Storage(server.URL, "test-key", "test-secret", "originals", "gallery")
	ctx := t.Context()
	empty := ""
	if missing, err := store.Read(ctx, "private", "catalog.json"); err != nil || missing != nil {
		t.Fatalf("Missing catalog: %v %v", missing, err)
	}
	if err := store.Write(ctx, "private", "catalog.json", []byte("initial"), "application/json", "no-store", &empty); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, "private", "catalog.json", []byte("duplicate"), "application/json", "no-store", &empty); !errors.Is(err, errConflict) {
		t.Fatalf("Create-once overwrote catalog: %v", err)
	}
	initial, err := store.Read(ctx, "private", "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, body := range []string{"first writer", "second writer"} {
		go func() {
			results <- store.Write(ctx, "private", "catalog.json", []byte(body), "application/json", "no-store", &initial.ETag)
		}()
	}
	wins, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, errConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("Expected one winner and one conflict, got %d/%d", wins, conflicts)
	}
	final, err := store.Read(ctx, "private", "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(final.Body) != "first writer" && string(final.Body) != "second writer" {
		t.Fatalf("Wrong stored body: %q", final.Body)
	}
	if err := store.Delete(ctx, "private", "catalog.json"); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Read(ctx, "private", "catalog.json"); err != nil || value != nil {
		t.Fatalf("R2 object still exists after deletion: %v", err)
	}
	if err := store.Delete(ctx, "private", "catalog.json"); err != nil {
		t.Fatalf("Deletion should tolerate an already removed object: %v", err)
	}
	if err := store.Delete(ctx, "public", "forbidden"); err == nil {
		t.Fatal("Delete permission failure was ignored")
	}
}

func TestR2CopyUsesServerSideCopyAndReplacesMetadata(t *testing.T) {
	for _, test := range []struct{ key, escaped string }{
		{"photos/id/1280.jpg", "photos/id/1280.jpg"},
		{"photos/summer holiday/1280.jpg", "photos/summer%20holiday/1280.jpg"},
		{"photos/雪 café/1280.jpg", "photos/%E9%9B%AA%20caf%C3%A9/1280.jpg"},
		{"photos/a+b/#?%2F&=.jpg", "photos/a%2Bb/%23%3F%252F%26%3D.jpg"},
	} {
		t.Run(test.key, func(t *testing.T) {
			type storedObject struct {
				body               []byte
				contentType, cache string
				metadata           http.Header
			}
			body := []byte{0xff, 0xd8, 0, 1, 0xff, 0xd9}
			const cache = "public, max-age=31536000, immutable"
			var mu sync.Mutex
			objects := map[string]storedObject{
				"originals/" + test.key: {body, "image/jpeg", "no-store", http.Header{"X-Amz-Meta-Private": {"private"}}},
				"gallery/" + test.key:   {[]byte("old destination"), "text/plain", "no-store", nil},
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests++
				if r.Method != http.MethodPut || r.URL.Path != "/gallery/"+test.key {
					t.Errorf("Expected only destination CopyObject PUT, got %s %s", r.Method, r.URL)
					w.WriteHeader(400)
					return
				}
				requestBody, err := io.ReadAll(r.Body)
				if err != nil || len(requestBody) != 0 {
					t.Errorf("Copy must not transfer image bytes: %x, %v", requestBody, err)
				}
				rawSource := r.Header.Get("X-Amz-Copy-Source")
				if rawSource != "originals/"+test.escaped {
					t.Errorf("Copy source = %q, want %q", rawSource, "originals/"+test.escaped)
				}
				source, err := url.PathUnescape(rawSource)
				value, exists := objects[source]
				if err != nil || !exists {
					t.Errorf("Copy source not found: %q, %v", source, err)
					w.WriteHeader(404)
					return
				}
				if r.Header.Get("X-Amz-Metadata-Directive") != "REPLACE" {
					t.Errorf("Private metadata must be replaced: %v", r.Header)
				} else {
					value.contentType = r.Header.Get("Content-Type")
					value.cache = r.Header.Get("Cache-Control")
					value.metadata = nil
				}
				objects[strings.TrimPrefix(r.URL.Path, "/")] = value
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<CopyObjectResult><ETag>"copied"</ETag></CopyObjectResult>`)
			}))
			defer server.Close()
			store := newR2Storage(server.URL, "test-key", "test-secret", "originals", "gallery")
			if err := store.Copy(t.Context(), "private", "public", test.key, "image/jpeg", cache); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 1 {
				t.Fatalf("Copy made %d requests; want one server-side operation", requests)
			}
			destination := objects["gallery/"+test.key]
			if !bytes.Equal(destination.body, body) || destination.contentType != "image/jpeg" || destination.cache != cache || len(destination.metadata) != 0 {
				t.Fatalf("Wrong copied bytes/metadata: %+v", destination)
			}
			source := objects["originals/"+test.key]
			if !bytes.Equal(source.body, body) || source.cache != "no-store" || source.metadata.Get("X-Amz-Meta-Private") != "private" {
				t.Fatalf("Source changed during copy: %+v", source)
			}
		})
	}
}

func TestR2CopyPropagatesErrors(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
	}{
		{"missing source", "NoSuchKey", 404},
		{"permission denied", "AccessDenied", 403},
		{"embedded copy failure", "InternalError", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(test.status)
				fmt.Fprintf(w, "<Error><Code>%s</Code><Message>copy failed</Message></Error>", test.code)
			}))
			defer server.Close()
			store := newR2Storage(server.URL, "test-key", "test-secret", "originals", "gallery")
			err := store.Copy(t.Context(), "private", "public", "photos/id/1280.jpg", "image/jpeg", "public, max-age=31536000, immutable")
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) || apiErr.ErrorCode() != test.code {
				t.Fatalf("Copy error = %v, want %s", err, test.code)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := store.Copy(ctx, "private", "public", "photos/id/1280.jpg", "image/jpeg", "no-store"); !errors.Is(err, context.Canceled) {
				t.Fatalf("Canceled copy = %v", err)
			}
		})
	}
}

func TestLocalCopyRetainsSourceAndPropagatesErrors(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	ctx := t.Context()
	const key = "photos/雪 café+a#%/1280.jpg"
	body := []byte{0xff, 0xd8, 0, 1, 0xff, 0xd9}
	if err := store.Write(ctx, "private", key, body, "image/jpeg", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 { // Also allow replacing an existing destination.
		if err := store.Copy(ctx, "private", "public", key, "image/jpeg", "public, max-age=31536000, immutable"); err != nil {
			t.Fatal(err)
		}
		for _, bucket := range []string{"private", "public"} {
			value, err := store.Read(ctx, bucket, key)
			if err != nil || value == nil || !bytes.Equal(value.Body, body) {
				t.Fatalf("Copy changed %s object: %v, %v", bucket, value, err)
			}
		}
	}
	if err := store.Copy(ctx, "private", "public", "missing.jpg", "image/jpeg", "no-store"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Missing source error = %v", err)
	}
	if value, err := store.Read(ctx, "public", "missing.jpg"); err != nil || value != nil {
		t.Fatalf("Missing source created destination: %v, %v", value, err)
	}
	for _, test := range []struct{ source, destination, key string }{
		{"invalid", "public", key},
		{"private", "invalid", key},
		{"private", "public", "../escape.jpg"},
		{"private", "public", "photos"}, // Reading a directory must fail.
	} {
		if err := store.Copy(ctx, test.source, test.destination, test.key, "image/jpeg", "no-store"); err == nil {
			t.Errorf("Ignored invalid copy: %+v", test)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Copy(canceled, "private", "public", key, "image/jpeg", "no-store"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Canceled copy = %v", err)
	}
	// A file blocking the destination directory reliably fails even when run as root.
	blocked := &localStorage{root: t.TempDir()}
	if err := blocked.Write(ctx, "private", key, body, "image/jpeg", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked.root, "public"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := blocked.Copy(ctx, "private", "public", key, "image/jpeg", "no-store"); err == nil {
		t.Fatal("Destination write error ignored")
	}
	if source, err := blocked.Read(ctx, "private", key); err != nil || source == nil || !bytes.Equal(source.Body, body) {
		t.Fatalf("Failed copy damaged source: %v, %v", source, err)
	}
}
