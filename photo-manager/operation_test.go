package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type pausedOperationStorage struct {
	storage
	paused  atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (s *pausedOperationStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if bucket == "private" && key == "catalog.json" && s.paused.Load() {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.release:
		}
	}
	return s.storage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func (b *browser) async(method, path string, value any) operation {
	b.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		b.t.Fatal(err)
	}
	request, err := http.NewRequest(method, b.server.URL+path, bytes.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	request.Header.Set("Origin", b.server.URL)
	request.Header.Set("If-Match", b.state.ETag)
	request.Header.Set("Prefer", "respond-async")
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 202 || response.Header.Get("Location") != "/api/operation" {
		body, _ := io.ReadAll(response.Body)
		b.t.Fatalf("Change was not accepted asynchronously: %d %s", response.StatusCode, body)
	}
	var accepted struct{ Operation operation }
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		b.t.Fatal(err)
	}
	return accepted.Operation
}

func (b *browser) waitOperation(state string) operation {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var result struct{ Operation operation }
		json.Unmarshal(b.request("GET", "/api/operation", nil, 200), &result)
		if result.Operation.State != "running" {
			if result.Operation.State != state {
				b.t.Fatalf("Change outcome: got %+v, want %s", result.Operation, state)
			}
			return result.Operation
		}
		if time.Now().After(deadline) {
			b.t.Fatal("Change did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAsyncAlbumDeletionAcknowledgesBeforeStorageCompletes(t *testing.T) {
	store := &pausedOperationStorage{storage: &localStorage{root: t.TempDir()}, started: make(chan struct{}, 1), release: make(chan struct{})}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	photo := b.state.Catalog.Photos[0]
	b.request("POST", "/api/albums", map[string]string{"title": "Remove only this album"}, 200)
	id := b.state.Catalog.Albums[0].ID
	b.request("PUT", "/api/albums/"+id, map[string]any{"title": "Remove only this album", "slug": "sample", "photoIds": []string{photo.ID}, "published": true}, 200)
	b.publish("succeeded")
	live := b.request("GET", "/media/index.json", nil, 200)
	store.paused.Store(true)
	b.client.Timeout = 2 * time.Second
	accepted := b.async("DELETE", "/api/albums/"+id, nil)
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Change did not continue after HTTP acknowledgement")
	}
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Albums) != 1 {
		t.Fatal("UI cannot report deletion complete before storage commits")
	}
	b.request("PATCH", "/api/photos/"+photo.ID, edit(photo), 423)
	b.request("POST", "/api/publish", nil, 423)
	b.client.CloseIdleConnections()
	close(store.release)
	if finished := b.waitOperation("succeeded"); finished.ID != accepted.ID {
		t.Fatal("The original change did not complete")
	}
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Albums) != 0 || len(b.state.Catalog.Photos) != 1 || b.state.Catalog.Photos[0].ID != photo.ID {
		t.Fatal("Album deletion removed photo records")
	}
	if !bytes.Equal(live, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Album deletion published unrelated changes")
	}
}

func TestAsyncPhotoChangesPreserveValidationAndReportInterruptedJobs(t *testing.T) {
	store := &pausedOperationStorage{storage: &localStorage{root: t.TempDir()}, started: make(chan struct{}, 1), release: make(chan struct{})}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	photo := b.state.Catalog.Photos[0]
	photo.Published = true // Publishing without alt text is allowed.
	b.async("PATCH", "/api/photos/"+photo.ID, edit(photo))
	b.waitOperation("succeeded")
	// The accepted job must still check If-Match when it actually executes.
	b.async("PATCH", "/api/photos/"+photo.ID, edit(photo))
	if failure := b.waitOperation("failed"); !strings.Contains(failure.Error, "library changed") {
		t.Fatalf("Lost optimistic concurrency error: %+v", failure)
	}
	b.request("GET", "/api/catalog", nil, 200)
	b.async("POST", "/api/photos/"+photo.ID+"/archive", nil)
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	if !b.state.Catalog.Photos[0].Archived || b.state.Catalog.Photos[0].Archiving {
		t.Fatal("Background archive did not finish cleanup")
	}
	b.async("POST", "/api/photos/"+photo.ID+"/restore", nil)
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	store.paused.Store(true)
	accepted := b.async("DELETE", "/api/photos/"+photo.ID, nil)
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not start")
	}
	b.stop()
	store.paused.Store(false)
	b = newBrowser(t, store)
	job := b.waitOperation("failed")
	if job.ID != accepted.ID || !strings.Contains(job.Error, "restarted") || len(b.state.Catalog.Photos) != 1 {
		t.Fatal("Restart silently lost/replayed an uncertain mutation")
	}
	b.async("DELETE", "/api/photos/"+photo.ID, nil)
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Photos) != 0 {
		t.Fatal("Explicit deletion retry did not complete")
	}
}
