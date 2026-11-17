package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const performancePhotoID = "11111111111111111111111111111111"

func fixtureCatalog(t *testing.T, s storage, bucket, key string, c catalog) string {
	t.Helper()
	body, err := encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), bucket, key, body, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	value, err := s.Read(t.Context(), bucket, key)
	if err != nil || value == nil {
		t.Fatalf("Read fixture: %v", err)
	}
	return value.ETag
}

func performanceHandler(t *testing.T, s storage, localRoot string) http.Handler {
	t.Helper()
	queue, err := openQueueStorage(filepath.Join(t.TempDir(), "queue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { queue.Close() })
	a := &app{store: s, queueStore: queue, background: t.Context(), config: configuration{
		AuthMode: "proxy", AllowedEmail: "admin@example.test", Origin: "https://studio.example.test", PrivateAlbums: true,
		LocalRoot: localRoot,
	}}
	return a.routes()
}

func performanceRequest(h http.Handler, method, path, etag string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-Auth-Request-Email", "admin@example.test")
	r.Header.Set("Origin", "https://studio.example.test")
	r.Header.Set("If-Match", etag)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requirePerformanceStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Got status/cache %d %q, want %d/no-store: %s", w.Code, w.Header().Get("Cache-Control"), status, w.Body)
	}
	if status != 307 && w.Header().Get("Location") != "" {
		t.Fatal("Rejected preview issued a capability")
	}
}

type membershipStorage struct {
	*localStorage
	reads        atomic.Int32
	failRead     atomic.Bool
	writeMode    atomic.Int32 // 1: fail before write, 2: lost response, 3: CAS conflict, 4: failed readback.
	pauseRead    atomic.Bool
	readStarted  chan struct{}
	readRelease  chan struct{}
	writeStarted chan struct{}
	writeRelease chan struct{}
}

func (s *membershipStorage) Read(ctx context.Context, bucket, key string) (*object, error) {
	if bucket == "private" && key == "catalog.json" {
		s.reads.Add(1)
		if s.failRead.Load() {
			return nil, errors.New("catalog unavailable")
		}
		value, err := s.localStorage.Read(ctx, bucket, key)
		if s.pauseRead.Swap(false) {
			close(s.readStarted)
			<-s.readRelease
		}
		return value, err
	}
	return s.localStorage.Read(ctx, bucket, key)
}

func (s *membershipStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if bucket != "private" || key != "catalog.json" {
		return s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected)
	}
	if s.writeStarted != nil {
		close(s.writeStarted)
		<-s.writeRelease
	}
	mode := s.writeMode.Load()
	if mode == 1 {
		return errors.New("write unavailable")
	}
	if mode == 3 {
		return errConflict
	}
	if err := s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected); err != nil {
		return err
	}
	if mode == 2 {
		return errors.New("committed but response lost")
	}
	if mode == 4 {
		s.failRead.Store(true)
	}
	return nil
}

func (s *membershipStorage) PreviewURL(ctx context.Context, key, method string) (string, error) {
	return "https://media.example.test/" + key + "?method=" + method, nil
}

func TestPreviewMembershipCoalescesAndInvalidatesSuccessfulWrites(t *testing.T) {
	s := &membershipStorage{localStorage: &localStorage{root: t.TempDir()}}
	etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", catalog{Version: 4, Photos: []photo{{ID: performancePhotoID, Kind: "video"}}})
	h := performanceHandler(t, s, "")
	var requests sync.WaitGroup
	for range 24 {
		requests.Go(func() {
			w := performanceRequest(h, "HEAD", "/api/photos/"+performancePhotoID+"/video", "", nil)
			if w.Code != 307 || w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Preview: %d %v", w.Code, w.Header())
			}
		})
	}
	requests.Wait()
	if got := s.reads.Load(); got != 1 {
		t.Fatalf("Concurrent warm previews read catalog %d times, want 1", got)
	}
	// Archiving must retain private preview access, then deletion must revoke it.
	w := performanceRequest(h, "POST", "/api/photos/"+performancePhotoID+"/archive", etag, nil)
	requirePerformanceStatus(t, w, 200)
	before := s.reads.Load()
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 307)
	if s.reads.Load() != before+1 {
		t.Fatal("Archive did not invalidate membership")
	}
	var state struct{ ETag string }
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	requirePerformanceStatus(t, performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, state.ETag, nil), 200)
	for _, suffix := range []string{"image", "video"} {
		requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/"+suffix, "", nil), 404)
	}
	// A cached empty membership must also be invalidated by an upload.
	w = performanceRequest(h, "GET", "/api/catalog", "", nil)
	json.Unmarshal(w.Body.Bytes(), &state)
	w = performanceRequest(h, "POST", "/api/photos", state.ETag, smallPNG(t))
	requirePerformanceStatus(t, w, 200)
	var uploaded struct{ Catalog catalog }
	json.Unmarshal(w.Body.Bytes(), &uploaded)
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+uploaded.Catalog.Photos[0].ID+"/image", "", nil), 307)
}

func TestPreviewMembershipInvalidatedOnFailedAndUncertainIntentWrites(t *testing.T) {
	for _, mode := range []int32{1, 2, 3, 4} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			s := &membershipStorage{localStorage: &localStorage{root: t.TempDir()}}
			etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", catalog{Version: 4, Photos: []photo{{ID: performancePhotoID, Kind: "video"}}})
			h := performanceHandler(t, s, "")
			requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 307)
			s.writeMode.Store(mode)
			status := 500
			if mode == 3 {
				status = 409
			}
			requirePerformanceStatus(t, performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, etag, nil), status)
			// A failed refresh must not reuse membership from before the write.
			s.failRead.Store(true)
			for range 2 {
				requirePerformanceStatus(t, performanceRequest(h, "HEAD", "/api/photos/"+performancePhotoID+"/video", "", nil), 500)
			}
			s.failRead.Store(false)
			status = 307
			if mode == 2 || mode == 4 {
				status = 404 // Intent committed, even though the mutation failed.
			}
			requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), status)
		})
	}
}

func TestPreviewReadCannotPopulateAcrossCatalogWrite(t *testing.T) {
	s := &membershipStorage{localStorage: &localStorage{root: t.TempDir()}, readStarted: make(chan struct{}), readRelease: make(chan struct{})}
	etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", catalog{Version: 1, Photos: []photo{{ID: performancePhotoID}}})
	h := performanceHandler(t, s, "")
	s.pauseRead.Store(true)
	preview := make(chan *httptest.ResponseRecorder, 1)
	go func() { preview <- performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil) }()
	<-s.readStarted
	deleted := make(chan *httptest.ResponseRecorder, 1)
	go func() { deleted <- performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, etag, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.reads.Load() < 2 {
		if time.Now().After(deadline) {
			close(s.readRelease)
			t.Fatal("Mutation did not load authoritative catalog during preview read")
		}
		time.Sleep(time.Millisecond)
	}
	close(s.readRelease)
	requirePerformanceStatus(t, <-preview, 307) // This check preceded the write.
	requirePerformanceStatus(t, <-deleted, 200)
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 404)
}

func TestPreviewWaitsForInFlightCatalogWrite(t *testing.T) {
	s := &membershipStorage{localStorage: &localStorage{root: t.TempDir()}, writeStarted: make(chan struct{}), writeRelease: make(chan struct{})}
	etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", catalog{Version: 1, Photos: []photo{{ID: performancePhotoID}}})
	h := performanceHandler(t, s, "")
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 307)
	s.writeMode.Store(2) // Commit deletion intent, then lose its response.
	deleted := make(chan *httptest.ResponseRecorder, 1)
	go func() { deleted <- performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, etag, nil) }()
	<-s.writeStarted
	preview := make(chan *httptest.ResponseRecorder, 1)
	go func() { preview <- performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil) }()
	select {
	case w := <-preview:
		close(s.writeRelease)
		t.Fatalf("Preview escaped in-flight write: %d", w.Code)
	case <-time.After(20 * time.Millisecond):
	}
	close(s.writeRelease)
	requirePerformanceStatus(t, <-deleted, 500)
	requirePerformanceStatus(t, <-preview, 404)
}

func TestPreviewSnapshotNeverUsedForMutations(t *testing.T) {
	s := &membershipStorage{localStorage: &localStorage{root: t.TempDir()}}
	c := catalog{Version: 1, Photos: []photo{{ID: performancePhotoID}}}
	etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", c)
	h := performanceHandler(t, s, "")
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 307)
	s.failRead.Store(true)
	requirePerformanceStatus(t, performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, etag, nil), 500)
	s.failRead.Store(false)
	// Deliberately change storage behind the app to prove mutation ETags never
	// come from the preview snapshot (external writers remain unsupported).
	c.Revision++
	next := fixtureCatalog(t, s.localStorage, "private", "catalog.json", c)
	requirePerformanceStatus(t, performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, etag, nil), 409)
	requirePerformanceStatus(t, performanceRequest(h, "DELETE", "/api/photos/"+performancePhotoID, next, nil), 200)
	requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+performancePhotoID+"/image", "", nil), 404)
}

func TestAlbumSetValidationPreservesOrderAndDuplicateContracts(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	p := append([]photo{}, b.state.Catalog.Photos...)
	b.request("POST", "/api/albums", map[string]string{"title": "Ordered"}, 200)
	id := b.state.Catalog.Albums[0].ID
	order := []string{p[2].ID, p[0].ID, p[1].ID}
	input := map[string]any{"title": "Ordered", "slug": "ordered", "photoIds": order, "coverId": p[0].ID}
	b.request("PUT", "/api/albums/"+id, input, 200)
	if !reflect.DeepEqual(b.state.Catalog.Albums[0].PhotoIDs, order) {
		t.Fatal("Set iteration changed album order")
	}
	etag := b.state.ETag
	for _, ids := range [][]string{{p[0].ID, p[0].ID}, {p[0].ID, "missing"}, {p[1].ID}} {
		input["photoIds"] = ids
		b.request("PUT", "/api/albums/"+id, input, 400)
	}
	b.request("PATCH", "/api/photos/"+p[0].ID, edit(p[0], "missing"), 400)
	b.request("GET", "/api/catalog", nil, 200)
	if b.state.ETag != etag || !reflect.DeepEqual(b.state.Catalog.Albums[0].PhotoIDs, order) {
		t.Fatal("Rejected validation changed catalog")
	}
	// Photo edits historically accept duplicate album IDs, but do not duplicate membership.
	b.request("PATCH", "/api/photos/"+p[0].ID, edit(p[0], id, id), 200)
	if !reflect.DeepEqual(b.state.Catalog.Albums[0].PhotoIDs, order) {
		t.Fatal("Repeated selected album changed existing order/membership")
	}
}

type redactionStorage struct {
	*localStorage
	writes map[string]int
}

func (s *redactionStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if bucket == "public" || bucket == "protected" {
		s.writes[bucket+"/"+key]++
		if expected == nil || (*expected == "" && key != "index.xml") {
			return errors.New("redaction must use CAS")
		}
		want := "public, max-age=30, must-revalidate"
		if bucket == "protected" {
			want = "private, no-store"
		}
		if cache != want {
			return errors.New("redaction changed cache policy")
		}
	}
	return s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func TestArchiveSkipsUnchangedHistoryButRedactsAllReferenceForms(t *testing.T) {
	s := &redactionStorage{localStorage: &localStorage{root: t.TempDir()}, writes: map[string]int{}}
	etag := fixtureCatalog(t, s.localStorage, "private", "catalog.json", catalog{Version: 1, Photos: []photo{{ID: performancePhotoID}}})
	before := map[string]*object{}
	for _, bucket := range []string{"public", "protected"} {
		for _, key := range []string{"index.json", "releases/photo.json", "releases/member.json", "releases/cover.json", "releases/absent.json"} {
			c := catalog{Version: 1, Revision: 37, PublishedAt: "original publication", Photos: []photo{{ID: "kept", Alt: "unrelated metadata"}}, Albums: []album{{ID: "album", PhotoIDs: []string{"kept"}, CoverID: "kept", PasswordHash: "preserve-credentials"}}}
			switch key {
			case "index.json", "releases/photo.json":
				c.Photos = append(c.Photos, photo{ID: performancePhotoID})
				c.Albums[0].PhotoIDs = []string{performancePhotoID, "kept"}
				c.Albums[0].CoverID = performancePhotoID
			case "releases/member.json":
				c.Albums[0].PhotoIDs = []string{performancePhotoID, "kept"}
			case "releases/cover.json":
				c.Albums[0].CoverID = performancePhotoID
			}
			fixtureCatalog(t, s.localStorage, bucket, key, c)
			if key == "releases/absent.json" {
				// Exact bytes (including unknown fields) must survive a no-op.
				body := []byte(`{"version":1,"photos":[],"albums":[],"futureField":"keep"}`)
				if err := s.localStorage.Write(t.Context(), bucket, key, body, "application/json", "no-store", nil); err != nil {
					t.Fatal(err)
				}
			}
			before[bucket+"/"+key], _ = s.localStorage.Read(t.Context(), bucket, key)
		}
	}
	h := performanceHandler(t, s, "")
	w := performanceRequest(h, "POST", "/api/photos/"+performancePhotoID+"/archive", etag, nil)
	requirePerformanceStatus(t, w, 200)
	for path, original := range before {
		bucket, key, _ := strings.Cut(path, "/")
		value, err := s.localStorage.Read(t.Context(), bucket, key)
		if err != nil || value == nil {
			t.Fatalf("Read redacted %s: %v", path, err)
		}
		if key == "releases/absent.json" {
			if s.writes[path] != 0 || !bytes.Equal(value.Body, original.Body) || value.ETag != original.ETag {
				t.Fatalf("Unchanged history rewritten: %s", path)
			}
			continue
		}
		var c catalog
		if err := json.Unmarshal(value.Body, &c); err != nil {
			t.Fatal(err)
		}
		if s.writes[path] != 1 || len(c.Photos) != 1 || c.Photos[0].ID != "kept" || c.Photos[0].Alt != "unrelated metadata" || !reflect.DeepEqual(c.Albums[0].PhotoIDs, []string{"kept"}) || c.Albums[0].CoverID != "kept" || c.Albums[0].PasswordHash != "preserve-credentials" || c.Revision != 37 || c.PublishedAt != "original publication" {
			t.Fatalf("Incorrect redaction of %s: %+v", path, c)
		}
	}
	var state struct{ ETag string }
	json.Unmarshal(w.Body.Bytes(), &state)
	// Retry archive: no public/protected manifests need another write.
	requirePerformanceStatus(t, performanceRequest(h, "POST", "/api/photos/"+performancePhotoID+"/archive", state.ETag, nil), 200)
	for path, count := range s.writes {
		if count != 1 {
			t.Fatalf("Retry rewrote already-redacted %s: %d writes", path, count)
		}
	}
}

type derivativeStorage struct {
	*localStorage
	started     chan string
	failRelease chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	fail        atomic.Bool
	active      atomic.Int32
	peak        atomic.Int32
}

func (s *derivativeStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if bucket == "private" && strings.HasPrefix(key, "photos/") {
		n := s.active.Add(1)
		defer s.active.Add(-1)
		for peak := s.peak.Load(); n > peak; peak = s.peak.Load() {
			if s.peak.CompareAndSwap(peak, n) {
				break
			}
		}
		if s.fail.Load() {
			s.started <- key
			if strings.HasSuffix(key, "/640.jpg") {
				<-s.failRelease
				return errors.New("derivative transfer failed")
			}
			if strings.HasSuffix(key, "/1280.jpg") {
				// A sibling can still commit bytes after cancellation. Leave them
				// private and undiscoverable; multipart retries overwrite this key.
				<-s.failRelease
				return s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected)
			}
			<-ctx.Done()
			// Deliberately delay sibling cancellation completion: the API must join it.
			s.cancelled <- struct{}{}
			<-s.release
			return ctx.Err()
		}
	}
	return s.localStorage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func TestDerivativeWritesAreConcurrentAndFailureNeverCommitsCatalog(t *testing.T) {
	for _, multipart := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "multipart"}[multipart], func(t *testing.T) {
			s := &derivativeStorage{localStorage: &localStorage{root: t.TempDir()}, started: make(chan string, 3), failRelease: make(chan struct{}), cancelled: make(chan struct{}, 2), release: make(chan struct{})}
			h := performanceHandler(t, s, s.root)
			body := smallPNG(t)
			path := "/api/photos"
			id := ""
			if multipart {
				w := performanceRequest(h, "POST", "/api/uploads", "new", []byte(`{"filename":"sample.png","size":`+strconv.Itoa(len(body))+`}`))
				requirePerformanceStatus(t, w, 200)
				var plan uploadPlan
				json.Unmarshal(w.Body.Bytes(), &plan)
				id = plan.Upload.ID
				requirePerformanceStatus(t, performanceRequest(h, "PUT", plan.Parts[0].URL, "", body), 200)
				path, body = "/api/uploads/"+id+"/complete", nil
			}
			s.fail.Store(true)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- performanceRequest(h, "POST", path, "new", body) }()
			for range 3 {
				select {
				case <-s.started:
				case <-time.After(5 * time.Second):
					close(s.failRelease)
					close(s.release)
					t.Fatal("Three derivative writes did not overlap")
				}
			}
			if s.peak.Load() != 3 {
				t.Fatalf("Concurrent derivative writes: %d", s.peak.Load())
			}
			close(s.failRelease)
			select {
			case <-s.cancelled:
			case <-time.After(5 * time.Second):
				close(s.release)
				t.Fatal("Failure did not cancel sibling transfer")
			}
			select {
			case <-done:
				close(s.release)
				t.Fatal("API returned before cancelled sibling writes joined")
			default:
			}
			close(s.release)
			requirePerformanceStatus(t, <-done, 500)
			if s.active.Load() != 0 {
				t.Fatal("API returned with derivative writes still running")
			}
			if value, err := s.localStorage.Read(t.Context(), "private", "catalog.json"); err != nil || value != nil {
				t.Fatal("Failed derivatives committed a catalog")
			}
			partial, err := s.List(t.Context(), "private", "photos/")
			if err != nil || len(partial) != 1 || !strings.HasSuffix(partial[0], "/1280.jpg") {
				t.Fatal("Unexpected partial transfer state")
			}
			partialID := strings.Split(partial[0], "/")[1]
			requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+partialID+"/image", "", nil), 404)
			if public, err := s.List(t.Context(), "public", ""); err != nil || len(public) != 0 {
				t.Fatal("Failed transfer made media public")
			}
			keys, err := s.List(t.Context(), "private", "originals/")
			if err != nil || len(keys) != 1 {
				t.Fatal("Failure lost original needed for retry/inspection")
			}
			s.fail.Store(false)
			w := performanceRequest(h, "POST", path, "new", body)
			requirePerformanceStatus(t, w, 200)
			var state struct{ Catalog catalog }
			json.Unmarshal(w.Body.Bytes(), &state)
			if len(state.Catalog.Photos) != 1 || state.Catalog.Photos[0].BlurHash == "" || (multipart && state.Catalog.Photos[0].ID != id) {
				t.Fatal("Retry failed to commit exactly one complete item with BlurHash")
			}
			for _, key := range state.Catalog.Photos[0].files() {
				value, err := s.Read(t.Context(), "private", key)
				if err != nil || value == nil || len(value.Body) == 0 {
					t.Fatalf("Retry missing derivative %s", key)
				}
			}
			requirePerformanceStatus(t, performanceRequest(h, "GET", "/api/photos/"+state.Catalog.Photos[0].ID+"/image", "", nil), 200)
		})
	}
}
