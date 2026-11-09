package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Exercise the real AWS SDK over HTTP against an S3 conditional-write fixture.
// This does not substitute for a live R2 credential/permissions acceptance test.
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
