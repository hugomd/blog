package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func (b *browser) queueCommand(action string, ids ...string) map[string]any {
	targets := []queueTarget{}
	for _, id := range ids {
		targets = append(targets, queueTarget{ID: id, Token: b.state.QueueTokens[id]})
	}
	return map[string]any{"id": fmt.Sprintf("%d-%s", time.Now().UnixMilli(), identifier()), "action": action, "targets": targets}
}

func (b *browser) waitQueue(id, state string) queueJob {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var result struct{ Queue queueView }
		json.Unmarshal(b.request("GET", "/api/queue", nil, 200), &result)
		for _, job := range result.Queue.Jobs {
			if job.ID == id && job.State == state {
				return job
			}
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("Queue did not reach %s: %+v", state, result.Queue)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueueAcceptsUnrelatedBatchesWhileRunningAndCancelsWaitingWork(t *testing.T) {
	store := &pausedOperationStorage{storage: &localStorage{root: t.TempDir()}, started: make(chan struct{}, 1), release: make(chan struct{})}
	b := newBrowser(t, store)
	for range 4 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	photos := b.state.Catalog.Photos
	first := b.queueCommand("archive", photos[0].ID, photos[2].ID)
	second := b.queueCommand("archive", photos[1].ID)
	cancelled := b.queueCommand("archive", photos[3].ID)
	store.paused.Store(true)
	b.request("POST", "/api/queue", first, 202)
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Archive did not start")
	}
	// The catalog writer is blocked on storage; submissions must not wait on it.
	b.client.Timeout = 2 * time.Second
	b.request("POST", "/api/queue", second, 202)
	b.request("POST", "/api/queue", first, 202) // Lost acknowledgement, same ID.
	b.request("POST", "/api/queue", b.queueCommand("archive", photos[0].ID), 409)
	b.request("POST", "/api/queue", cancelled, 202)
	b.request("POST", "/api/queue/"+cancelled["id"].(string)+"/cancel", nil, 202)
	b.request("POST", "/api/queue/"+first["id"].(string)+"/cancel", nil, 409)
	b.request("PATCH", "/api/photos/"+photos[3].ID, edit(photos[3]), 423)
	b.request("POST", "/api/publish", nil, 423)
	saved, err := b.queue.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Jobs) != 3 || saved.Jobs[0].ID != first["id"] || saved.Jobs[1].ID != second["id"] {
		t.Fatalf("Lost acceptance ordering or deduplication: %+v", saved)
	}
	close(store.release)
	b.waitQueue(second["id"].(string), "succeeded")
	b.waitQueue(cancelled["id"].(string), "cancelled")
	b.request("GET", "/api/catalog", nil, 200)
	for i, p := range b.state.Catalog.Photos {
		if p.Archived != (i != 3) || p.Archiving {
			t.Fatalf("Wrong batch effect: %+v", p)
		}
	}
	// Restore is also a batch and must not publish automatically.
	restore := b.queueCommand("restore", photos[0].ID, photos[2].ID)
	b.request("POST", "/api/queue", restore, 202)
	b.waitQueue(restore["id"].(string), "succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	for i, p := range b.state.Catalog.Photos {
		if p.Archived != (i == 1) || p.Published {
			t.Fatalf("Wrong restore effect: %+v", p)
		}
	}
	b.request("POST", "/api/queue", first, 202) // Must not rearchive after restore.
	b.request("GET", "/api/catalog", nil, 200)
	if b.state.Catalog.Photos[0].Archived {
		t.Fatal("Duplicate submission reapplied an old archive")
	}
}

// Fail or pause at real storage boundaries, including AFTER a write commits.
type queueFaultStorage struct {
	*localStorage
	fault     func(context.Context, string, string, []byte, bool) error
	readFault func(context.Context, string, string) error
}

func (s *queueFaultStorage) WriteFile(ctx context.Context, bucket, key string, file *os.File, contentType, cache string, expected *string) error {
	body, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.fault(ctx, bucket, key, body, false); err != nil {
		return err
	}
	if err := s.localStorage.WriteFile(ctx, bucket, key, file, contentType, cache, expected); err != nil {
		return err
	}
	return s.fault(ctx, bucket, key, body, true)
}

func (s *queueFaultStorage) Read(ctx context.Context, bucket, key string) (*object, error) {
	if s.readFault != nil {
		if err := s.readFault(ctx, bucket, key); err != nil {
			return nil, err
		}
	}
	return s.localStorage.Read(ctx, bucket, key)
}

type faultQueueStorage struct {
	queueStorage
	storage *queueFaultStorage
}

func (s *faultQueueStorage) Commit(ctx context.Context, commit queueCommit) error {
	body, _ := encode(commit.Job)
	if err := s.storage.fault(ctx, "sqlite", "queue.sqlite", body, false); err != nil {
		return err
	}
	if err := s.queueStorage.Commit(ctx, commit); err != nil {
		return err
	}
	return s.storage.fault(ctx, "sqlite", "queue.sqlite", body, true)
}

func (s *queueFaultStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if err := s.fault(ctx, bucket, key, body, false); err != nil {
		return err
	}
	if err := s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected); err != nil {
		return err
	}
	return s.fault(ctx, bucket, key, body, true)
}

type latePublicationStorage struct {
	*localStorage
	bucket, key string
	armed       atomic.Bool
	late        func() error
}

func (s *latePublicationStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if bucket == s.bucket && strings.HasPrefix(key, s.key) && s.armed.CompareAndSwap(true, false) {
		if expected == nil {
			return errors.New("publication manifest was not conditional")
		}
		copyBody, copyETag := bytes.Clone(body), *expected
		s.late = func() error {
			return s.localStorage.Write(context.Background(), bucket, key, copyBody, contentType, cache, &copyETag)
		}
		return errors.New("remote PUT remains outstanding")
	}
	return s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func TestLatePublicationCannotUndoArchiveAfterRestart(t *testing.T) {
	for _, target := range []struct{ bucket, key string }{{"public", "index.json"}, {"public", "index.xml"}, {"public", "releases/"}, {"protected", "releases/"}} {
		t.Run(target.bucket+"-"+target.key, func(t *testing.T) {
			s := &latePublicationStorage{localStorage: &localStorage{root: t.TempDir()}, bucket: target.bucket, key: target.key}
			b := newBrowser(t, s)
			b.raw("POST", "/api/photos", smallPNG(t), 200)
			p := b.state.Catalog.Photos[0]
			p.Published = true
			b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
			c := b.state.Catalog
			c.Albums = []album{{ID: identifier(), Access: "password", PasswordHash: "test-hash", AccessVersion: "test-version", Published: true, PhotoIDs: []string{p.ID}, CoverID: p.ID}}
			fixtureCatalog(t, s, "private", "catalog.json", c)
			b.request("GET", "/api/catalog", nil, 200)
			s.armed.Store(true)
			b.publish("failed")
			if s.late == nil {
				t.Fatal("Did not retain a late conditional publication")
			}
			b.stop()
			b = newBrowser(t, s)
			command := b.queueCommand("archive", p.ID)
			b.request("POST", "/api/queue", command, 202)
			b.waitQueue(command["id"].(string), "succeeded")
			if err := s.late(); !errors.Is(err, errConflict) {
				t.Fatalf("Late publication was not fenced: %v", err)
			}
			feed, err := s.Read(t.Context(), "public", "index.xml")
			if err != nil || (feed != nil && bytes.Contains(feed.Body, []byte(p.ID))) {
				t.Fatalf("Archive left the photo in RSS: %v", err)
			}
			if target.key == "index.xml" && feed == nil {
				t.Fatal("Archive did not replace the pending RSS publication")
			}
			for _, bucket := range []string{"public", "protected"} {
				keys, err := s.List(t.Context(), bucket, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range keys {
					if !strings.HasSuffix(key, ".json") {
						continue
					}
					value, err := s.Read(t.Context(), bucket, key)
					if err != nil {
						t.Fatal(err)
					}
					var live catalog
					if err := json.Unmarshal(value.Body, &live); err != nil {
						t.Fatal(err)
					}
					for _, published := range live.Photos {
						if published.ID == p.ID {
							t.Fatalf("Archive undone in %s/%s", bucket, key)
						}
					}
				}
			}
		})
	}
}

func TestCatalogCannotPairStalePhotosWithCompletedQueue(t *testing.T) {
	var armed atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	s := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}, fault: func(context.Context, string, string, []byte, bool) error { return nil }}
	s.readFault = func(ctx context.Context, bucket, key string) error {
		if bucket == "public" && key == "index.json" && armed.CompareAndSwap(true, false) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	b := newBrowser(t, s)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	command := b.queueCommand("archive", b.state.Catalog.Photos[0].ID)
	armed.Store(true)
	responses := make(chan *http.Response, 1)
	go func() { response, _ := b.client.Get(b.server.URL + "/api/catalog"); responses <- response }()
	<-started
	b.request("POST", "/api/queue", command, 202)
	b.waitQueue(command["id"].(string), "succeeded")
	close(release)
	response := <-responses
	if response == nil {
		t.Fatal("Catalog request failed")
	}
	defer response.Body.Close()
	var stale struct {
		Catalog catalog
		Queue   queueView
	}
	if err := json.NewDecoder(response.Body).Decode(&stale); err != nil {
		t.Fatal(err)
	}
	if !stale.Catalog.Photos[0].Archived {
		for _, job := range stale.Queue.Jobs {
			if job.State == "succeeded" {
				t.Fatal("Stale catalog claimed archive completed")
			}
		}
	}
	var fresh struct {
		Catalog catalog
		Queue   queueView
	}
	json.Unmarshal(b.request("GET", "/api/catalog", nil, 200), &fresh)
	if !fresh.Catalog.Photos[0].Archived || len(fresh.Queue.Jobs) != 1 || fresh.Queue.Jobs[0].State != "succeeded" {
		t.Fatal("Subsequent poll did not converge")
	}
}

func TestArchiveResumesHistoryAfterCheckpoint(t *testing.T) {
	var armed, restarted atomic.Bool
	paused := make(chan struct{}, 1)
	s := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}}
	s.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if bucket == "public" && key == "releases/025.json" && !committed && armed.CompareAndSwap(true, false) {
			paused <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	s.readFault = func(ctx context.Context, bucket, key string) error {
		if restarted.Load() && bucket == "public" && strings.HasPrefix(key, "releases/") && key < "releases/025.json" {
			return errors.New("completed history cannot be read again")
		}
		return nil
	}
	b := newBrowser(t, s)
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	photos := b.state.Catalog.Photos
	live := catalog{Version: 1, Photos: photos, Albums: []album{{ID: "album", PhotoIDs: []string{photos[0].ID, photos[1].ID, photos[2].ID}, CoverID: photos[0].ID}}}
	for _, bucket := range []string{"public", "protected"} {
		for i := range 55 {
			fixtureCatalog(t, s, bucket, fmt.Sprintf("releases/%03d.json", i), live)
		}
	}
	command := b.queueCommand("archive", photos[0].ID, photos[2].ID)
	armed.Store(true)
	b.request("POST", "/api/queue", command, 202)
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("Archive did not reach second page")
	}
	b.stop()
	restarted.Store(true)
	b = newBrowser(t, s)
	b.waitQueue(command["id"].(string), "succeeded")
	for _, bucket := range []string{"public", "protected"} {
		for i := range 55 {
			value, err := s.localStorage.Read(t.Context(), bucket, fmt.Sprintf("releases/%03d.json", i))
			if err != nil {
				t.Fatal(err)
			}
			var after catalog
			json.Unmarshal(value.Body, &after)
			if len(after.Photos) != 1 || after.Photos[0].ID != photos[1].ID || after.Albums[0].CoverID != photos[1].ID {
				t.Fatalf("Wrong redaction at %s/%d", bucket, i)
			}
		}
	}
}

func TestQueueRecoversEveryArchiveCommitBoundaryAfterRestart(t *testing.T) {
	for _, boundary := range []string{"accepted", "intent", "public", "rss-before", "rss-after", "protected", "done", "result"} {
		t.Run(boundary, func(t *testing.T) {
			var armed atomic.Bool
			paused := make(chan struct{}, 1)
			store := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}}
			store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
				match := (boundary == "accepted" && key == "queue.sqlite" && bytes.Contains(body, []byte(`"state": "running"`)) && !committed) ||
					(boundary == "intent" && key == "catalog.json" && bytes.Contains(body, []byte(":applying")) && committed) ||
					(boundary == "done" && key == "catalog.json" && bytes.Contains(body, []byte(":done")) && committed) ||
					(boundary == "public" && bucket == "public" && key == "index.json" && committed) ||
					(boundary == "rss-before" && bucket == "public" && key == "index.xml" && !committed) ||
					(boundary == "rss-after" && bucket == "public" && key == "index.xml" && committed) ||
					(boundary == "protected" && bucket == "protected" && key == "index.json" && committed) ||
					(boundary == "result" && key == "queue.sqlite" && bytes.Contains(body, []byte(`"state": "succeeded"`)) && !committed)
				if match && armed.CompareAndSwap(true, false) {
					paused <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			b := newBrowser(t, store)
			for range 3 {
				b.raw("POST", "/api/photos", smallPNG(t), 200)
			}
			photos := b.state.Catalog.Photos
			live := catalog{Version: 1, Photos: photos, Albums: []album{{ID: "album", PhotoIDs: []string{photos[0].ID, photos[1].ID, photos[2].ID}, CoverID: photos[0].ID}}}
			for _, bucket := range []string{"public", "protected"} {
				for _, key := range []string{"index.json", "releases/older.json"} {
					body, _ := encode(live)
					if err := store.Write(t.Context(), bucket, key, body, "application/json", "no-store", nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			command := b.queueCommand("archive", photos[0].ID, photos[2].ID)
			armed.Store(true)
			b.request("POST", "/api/queue", command, 202)
			select {
			case <-paused:
			case <-time.After(5 * time.Second):
				t.Fatal("Did not reach interruption boundary")
			}
			b.stop()
			b = newBrowser(t, store)
			b.waitQueue(command["id"].(string), "succeeded")
			b.request("GET", "/api/catalog", nil, 200)
			if b.state.Catalog.Version != 5 || !b.state.Catalog.Photos[0].Archived || b.state.Catalog.Photos[0].Archiving || b.state.Catalog.Photos[1].Archived {
				t.Fatalf("Wrong recovered catalog: %+v", b.state.Catalog)
			}
			var feed rssFeed
			if err := xml.Unmarshal(b.request("GET", "/media/index.xml", nil, 200), &feed); err != nil {
				t.Fatal(err)
			}
			if len(feed.Channel.Items) != 1 || feed.Channel.Items[0].GUID != "https://example.test/photos/"+photos[1].ID+"/" {
				t.Fatalf("Incomplete RSS redaction after restart: %+v", feed.Channel.Items)
			}
			for _, bucket := range []string{"public", "protected"} {
				for _, key := range []string{"index.json", "releases/older.json"} {
					value, _ := store.Read(t.Context(), bucket, key)
					var redacted catalog
					json.Unmarshal(value.Body, &redacted)
					if len(redacted.Photos) != 1 || redacted.Photos[0].ID != photos[1].ID || redacted.Albums[0].CoverID != photos[1].ID {
						t.Fatalf("Incomplete redaction in %s/%s: %+v", bucket, key, redacted)
					}
				}
			}
			for _, p := range photos {
				if value, err := store.Read(t.Context(), "private", "originals/"+p.ID); err != nil || value == nil {
					t.Fatal("Archive removed an original")
				}
			}
		})
	}
}

func TestQueueFailurePausesFollowingWorkUntilExplicitRetry(t *testing.T) {
	var failing atomic.Bool
	store := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}}
	store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if failing.Load() && bucket == "public" && key == "index.json" && !committed {
			return errors.New("temporary storage outage")
		}
		return nil
	}
	b := newBrowser(t, store)
	for range 2 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	photos := b.state.Catalog.Photos
	body, _ := encode(catalog{Version: 1, Photos: photos, Albums: []album{}})
	store.Write(t.Context(), "public", "index.json", body, "application/json", "no-store", nil)
	failing.Store(true)
	first, second := b.queueCommand("archive", photos[0].ID), b.queueCommand("archive", photos[1].ID)
	b.request("POST", "/api/queue", first, 202)
	b.request("POST", "/api/queue", second, 202)
	failed := b.waitQueue(first["id"].(string), "failed")
	if failed.Attempts != 3 || failed.CanCancel {
		t.Fatalf("Expected bounded retries and non-cancellable cleanup: %+v", failed)
	}
	b.waitQueue(second["id"].(string), "queued")
	b.request("POST", "/api/queue/"+failed.ID+"/cancel", nil, 409)
	b.request("POST", "/api/publish", nil, 423)
	failing.Store(false)
	b.request("POST", "/api/queue/"+failed.ID+"/retry", nil, 202)
	b.waitQueue(second["id"].(string), "succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	if !b.state.Catalog.Photos[0].Archived || !b.state.Catalog.Photos[1].Archived {
		t.Fatal("Retry did not drain queue")
	}
}

func TestQueueValidatesWholeSelectionAndRequestIdentity(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	for range 2 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	p := b.state.Catalog.Photos[0]
	stale := b.queueCommand("archive", p.ID)
	p.Caption = "Changed in another tab"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/queue", stale, 409)
	b.request("POST", "/api/queue", b.queueCommand("archive", p.ID, p.ID), 400)
	missing := b.queueCommand("archive", p.ID)
	missing["targets"] = append(missing["targets"].([]queueTarget), queueTarget{ID: "missing", Token: "missing"})
	b.request("POST", "/api/queue", missing, 409)
	expired := b.queueCommand("archive", p.ID)
	expired["id"] = fmt.Sprintf("%d-%s", time.Now().Add(-25*time.Hour).UnixMilli(), identifier())
	b.request("POST", "/api/queue", expired, 409)
	command := b.queueCommand("archive", p.ID)
	b.request("POST", "/api/queue", command, 202)
	b.waitQueue(command["id"].(string), "succeeded")
	command["action"] = "restore"
	b.request("POST", "/api/queue", command, 409)
	b.request("GET", "/api/catalog", nil, 200)
	if !b.state.Catalog.Photos[0].Archived || b.state.Catalog.Photos[1].Archived || !strings.HasSuffix(b.state.Catalog.QueueReceipt, ":done") {
		t.Fatal("Invalid selection changed a different photo")
	}
}

func TestQueueRecoveryFencesAnUncertainSubmission(t *testing.T) {
	var failing atomic.Bool
	store := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}}
	store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if failing.Load() && key == "queue.sqlite" && !committed {
			return errors.New("connection lost before outcome known")
		}
		return nil
	}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	command := b.queueCommand("archive", b.state.Catalog.Photos[0].ID)
	before, _ := b.queue.Load(t.Context())
	failing.Store(true)
	b.request("POST", "/api/queue", command, 503)
	b.request("POST", "/api/publish", nil, 423)
	failing.Store(false)
	b.request("POST", "/api/queue/recover", nil, 202)
	// A stale transaction must fail even when recovery kept the same jobs.
	if err := b.queue.Commit(t.Context(), queueCommit{Expected: before.Generation, Generation: identifier()}); !errors.Is(err, errConflict) {
		t.Fatalf("Late pre-recovery write was not fenced: %v", err)
	}
	b.request("POST", "/api/queue", command, 202)
	b.waitQueue(command["id"].(string), "succeeded")
}

func TestQueueRestoreRecognizesCommittedResultAfterRestart(t *testing.T) {
	var armed atomic.Bool
	paused := make(chan struct{}, 1)
	store := &queueFaultStorage{localStorage: &localStorage{root: t.TempDir()}}
	store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if key == "catalog.json" && committed && bytes.Contains(body, []byte(":done")) && armed.CompareAndSwap(true, false) {
			paused <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	id := b.state.Catalog.Photos[0].ID
	b.request("POST", "/api/photos/"+id+"/archive", nil, 200)
	command := b.queueCommand("restore", id)
	armed.Store(true)
	b.request("POST", "/api/queue", command, 202)
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("Restore did not commit")
	}
	b.stop()
	value, _ := store.Read(t.Context(), "private", "catalog.json")
	b = newBrowser(t, store)
	b.waitQueue(command["id"].(string), "succeeded")
	after, _ := store.Read(t.Context(), "private", "catalog.json")
	if !bytes.Equal(value.Body, after.Body) {
		t.Fatal("Recovery reapplied an already committed restore")
	}
}

func TestQueueCapacityNeverDropsAcceptedJobs(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	command := b.queueCommand("archive", b.state.Catalog.Photos[0].ID)
	b.stop()
	// Seed a full durable backlog with a paused head, as if an outage persisted
	// across a pod replacement. No browser may make it silently lose old jobs.
	q := durableQueue{Version: 1, Jobs: []queueJob{}}
	for i := range queueLimit {
		q.Jobs = append(q.Jobs, queueJob{ID: fmt.Sprintf("%d-%s", time.Now().UnixMilli(), identifier()), Action: "archive", State: "queued", Targets: []queueTarget{{ID: fmt.Sprint(i), Token: "fixture"}}, CanCancel: true})
	}
	q.Jobs[0].State = "failed"
	seed, err := openQueueStorage(filepath.Join(store.root, "queue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := seed.Load(t.Context())
	for _, job := range q.Jobs {
		generation := identifier()
		if err := seed.Commit(t.Context(), queueCommit{Expected: previous.Generation, Generation: generation, Job: &job}); err != nil {
			t.Fatal(err)
		}
		previous.Generation = generation
	}
	seed.Close()
	b = newBrowser(t, store)
	b.request("POST", "/api/queue", command, 429)
	after, err := b.queue.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Jobs) != queueLimit || after.Jobs[0].ID != q.Jobs[0].ID || after.Jobs[queueLimit-1].ID != q.Jobs[queueLimit-1].ID {
		t.Fatal("A full queue lost previously accepted work")
	}
}
