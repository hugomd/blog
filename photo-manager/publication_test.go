package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Hold actual image copies, not the HTTP handler, to exercise a slow bucket.
type pausedCopyStorage struct {
	storage
	paused  bool
	started chan string
	release chan struct{}
}

func (s *pausedCopyStorage) Copy(ctx context.Context, source, destination, key, contentType, cache string) error {
	if s.paused {
		select {
		case s.started <- key:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.storage.Copy(ctx, source, destination, key, contentType, cache)
}

func TestPublicationRunsOutsideRequestAndResumesAfterRestart(t *testing.T) {
	store := &pausedCopyStorage{storage: &localStorage{root: t.TempDir()}, started: make(chan string, 6), release: make(chan struct{})}
	b := newBrowser(t, store)
	for i := range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
		p := b.state.Catalog.Photos[i]
		p.Alt, p.Published = "A quiet street", true
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
		if i == 0 {
			b.publish("succeeded")
		}
	}
	before := b.request("GET", "/media/index.json", nil, 200)
	store.paused = true
	b.client.Timeout = 2 * time.Second
	accepted := b.request("POST", "/api/publish", nil, http.StatusAccepted)
	var first struct{ Publication publication }
	if err := json.Unmarshal(accepted, &first); err != nil {
		t.Fatal(err)
	}
	// Two independently blocked copies prove that copying is concurrent.
	for range 2 {
		select {
		case <-store.started:
		case <-time.After(5 * time.Second):
			t.Fatal("Image copies did not start independently of the completed request")
		}
	}
	b.request("GET", "/api/catalog", nil, 200)
	var duplicate struct{ Publication publication }
	json.Unmarshal(b.request("POST", "/api/publish", nil, http.StatusAccepted), &duplicate)
	if duplicate.Publication.ID != first.Publication.ID || duplicate.Publication.State != "running" || duplicate.Publication.Total != 6 {
		t.Fatalf("Duplicate request did not return the running six-image job: %+v", duplicate)
	}
	id := b.state.Catalog.Photos[0].ID
	b.request("PATCH", "/api/photos/"+id, edit(b.state.Catalog.Photos[0]), 423)
	b.request("DELETE", "/api/photos/"+id, nil, 423)
	b.request("POST", "/api/photos/"+id+"/archive", nil, 423)
	if !bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Incomplete copies changed the discoverable gallery")
	}
	b.stop()
	// A cancelled process leaves a durable job, not a failed publication.
	record, err := store.Read(t.Context(), "private", "publication.json")
	if err != nil || record == nil || !bytes.Contains(record.Body, []byte(`"state": "running"`)) {
		t.Fatalf("No resumable publication record: %v", err)
	}
	store.paused = false
	b = newBrowser(t, store)
	job := b.waitPublication("succeeded")
	if job.ID != first.Publication.ID || job.Completed != 6 || job.Total != 6 {
		t.Fatalf("Restart did not finish the original job: %+v", job)
	}
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 3 || live.Revision != b.state.Catalog.Revision {
		t.Fatalf("Wrong final publication: %+v", live)
	}
	for _, p := range live.Photos {
		for _, size := range []string{"640", "1280", "2560"} {
			key := "photos/" + p.ID + "/" + size + ".jpg"
			private, err := store.Read(t.Context(), "private", key)
			if err != nil || private == nil {
				t.Fatalf("Private source missing: %s: %v", key, err)
			}
			public := b.request("GET", "/media/"+key, nil, 200)
			if !bytes.Equal(private.Body, public) {
				t.Fatalf("Copy changed image bytes: %s", key)
			}
		}
	}
}

func TestBackgroundPublicationPreservesConflictingLiveIndex(t *testing.T) {
	store := &pausedCopyStorage{storage: &localStorage{root: t.TempDir()}, paused: true, started: make(chan string, 3), release: make(chan struct{})}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt, p.Published = "Private source", true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/publish", nil, http.StatusAccepted)
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Copy did not start")
	}
	external := []byte(`{"version":1,"revision":99,"photos":[],"albums":[]}`)
	if err := store.Write(t.Context(), "public", "index.json", external, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	b.waitPublication("failed")
	if !bytes.Equal(external, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Publication overwrote an independently changed live index")
	}
}

func TestResumedPublicationRejectsChangedCatalog(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	b.stop()
	// Simulate a different catalog being restored while the service is stopped.
	body, _ := encode(publication{ID: "interrupted", Revision: 0, CatalogETag: "old-catalog", State: "running"})
	if err := store.Write(context.Background(), "private", "publication.json", body, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	b = newBrowser(t, store)
	job := b.waitPublication("failed")
	if job.ID != "interrupted" || job.Error == "" {
		t.Fatalf("Changed source was not rejected: %+v", job)
	}
	b.request("GET", "/media/index.json", nil, 404)
}
