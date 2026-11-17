package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type pausedBatchStorage struct {
	*localStorage
	pause *pausedOperationStorage
}

func (s *pausedBatchStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	return s.pause.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func TestUploadBatchProcessesAfterTransferWithoutBrowser(t *testing.T) {
	for _, invalidMiddle := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalidMiddle=%t", invalidMiddle), func(t *testing.T) {
			local := &localStorage{root: t.TempDir()}
			pause := &pausedOperationStorage{storage: local, started: make(chan struct{}, 1), release: make(chan struct{})}
			b := newBrowser(t, &pausedBatchStorage{local, pause})
			var ids, kept []string
			for i, name := range []string{"first.png", "middle.png", "last.png"} {
				body := smallPNG(t)
				if invalidMiddle && i == 1 {
					body = []byte("This is not an image")
				}
				plan := b.planUpload(name, len(body))
				b.transfer(plan, body)
				ids = append(ids, plan.Upload.ID)
				if !invalidMiddle || i != 1 {
					kept = append(kept, plan.Upload.ID)
				}
			}
			b.request("GET", "/api/catalog", nil, 200)
			if len(b.state.Catalog.Photos) != 0 {
				t.Fatal("Transferring the batch should not wait for or start conversion")
			}
			pause.paused.Store(true)
			b.client.Timeout = 2 * time.Second
			accepted := b.async("POST", "/api/uploads/process", map[string]any{"ids": ids})
			select {
			case <-pause.started:
			case <-time.After(5 * time.Second):
				t.Fatal("Batch did not continue after acknowledgement")
			}
			var progress struct {
				Operation struct{ Completed, Total int }
			}
			json.Unmarshal(b.request("GET", "/api/operation", nil, 200), &progress)
			if progress.Operation.Completed != 0 || progress.Operation.Total != 3 {
				t.Fatalf("Expected accepted batch progress, got %+v", progress)
			}
			b.request("GET", "/api/catalog", nil, 200)
			if len(b.state.Catalog.Photos) != 0 {
				t.Fatal("Processing was reported complete before the catalog write")
			}
			b.request("POST", "/api/uploads", map[string]any{"filename": "another.png", "size": 10}, 423)
			b.request("POST", "/api/publish", nil, 423)
			b.client.CloseIdleConnections()
			close(pause.release)
			outcome := "succeeded"
			if invalidMiddle {
				outcome = "failed"
			}
			finished := b.waitOperation(outcome)
			if finished.ID != accepted.ID || (invalidMiddle && (!strings.Contains(finished.Error, "middle.png") || !strings.Contains(finished.Error, "1 of 3"))) {
				t.Fatalf("Batch lost its outcome or per-file error: %+v", finished)
			}
			json.Unmarshal(b.request("GET", "/api/operation", nil, 200), &progress)
			if progress.Operation.Completed != 3 || progress.Operation.Total != 3 {
				t.Fatalf("Batch progress was lost on completion: %+v", progress)
			}
			b.request("GET", "/api/catalog", nil, 200)
			if len(b.state.Catalog.Photos) != len(kept) {
				t.Fatal("A failed file prevented later uploads from being processed")
			}
			for i, p := range b.state.Catalog.Photos {
				if p.ID != kept[i] || p.Published || p.BlurHash == "" {
					t.Fatalf("Lost or published an uploaded private draft: %+v", p)
				}
			}
			for _, target := range [][2]string{{"private", "uploads/"}, {"private", "multipart/"}, {"public", ""}} {
				keys, err := local.List(t.Context(), target[0], target[1])
				if err != nil || len(keys) != 0 {
					t.Fatalf("Unexpected leftover/public objects: %v %v", keys, err)
				}
			}
			revision := b.state.Catalog.Revision
			b.stop()
			b = newBrowser(t, local)
			if restored := b.waitOperation(outcome); restored.ID != accepted.ID {
				t.Fatal("Completed batch outcome did not survive restart")
			}
			b.async("POST", "/api/uploads/process", map[string]any{"ids": kept})
			b.waitOperation("succeeded")
			b.request("GET", "/api/catalog", nil, 200)
			if b.state.Catalog.Revision != revision || len(b.state.Catalog.Photos) != len(kept) {
				t.Fatal("Explicit batch retry duplicated already committed uploads")
			}
		})
	}
}

type interruptedUploadStorage struct {
	*localStorage
	failure string
}

func (s *interruptedUploadStorage) AbortUpload(ctx context.Context, key, id string) error {
	if s.failure == "abort" {
		return errors.New("multipart cleanup interrupted")
	}
	return s.localStorage.AbortUpload(ctx, key, id)
}

func (s *interruptedUploadStorage) Delete(ctx context.Context, bucket, key string) error {
	if s.failure == "metadata" && strings.HasPrefix(key, "uploads/") {
		return errors.New("upload metadata cleanup interrupted")
	}
	return s.localStorage.Delete(ctx, bucket, key)
}

func TestMultipartCompletionResumesCleanupAfterRestart(t *testing.T) {
	for _, failure := range []string{"abort", "metadata"} {
		t.Run(failure, func(t *testing.T) {
			local := &localStorage{root: t.TempDir()}
			b := newBrowser(t, local)
			body := smallPNG(t)
			plan := b.planUpload("retry.png", len(body))
			b.transfer(plan, body)
			b.stop()
			broken := newBrowser(t, &interruptedUploadStorage{local, failure})
			broken.request("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil, 500)
			broken.request("GET", "/api/catalog", nil, 200)
			if len(broken.state.Catalog.Photos) != 1 {
				t.Fatal("Expected committed item despite interrupted cleanup")
			}
			revision := broken.state.Catalog.Revision
			broken.stop()
			restarted := newBrowser(t, local)
			restarted.request("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil, 200)
			restarted.request("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil, 200)
			if len(restarted.state.Catalog.Photos) != 1 || restarted.state.Catalog.Revision != revision {
				t.Fatal("Cleanup retry changed the committed catalog")
			}
			for _, prefix := range []string{"uploads/", "multipart/"} {
				keys, err := local.List(t.Context(), "private", prefix)
				if err != nil || len(keys) != 0 {
					t.Fatalf("Cleanup left %s: %v %v", prefix, keys, err)
				}
			}
			for _, key := range append(restarted.state.Catalog.Photos[0].files(), "originals/"+plan.Upload.ID) {
				if value, err := local.Read(t.Context(), "private", key); err != nil || value == nil {
					t.Fatalf("Cleanup removed committed media %s", key)
				}
			}
		})
	}
}

type uploadPlan struct {
	Upload   struct{ ID, RequestID string }
	PartSize int64
	Parts    []struct {
		Number int
		URL    string
	}
}

func (b *browser) planUpload(filename string, size int) uploadPlan {
	b.t.Helper()
	var plan uploadPlan
	body := b.request("POST", "/api/uploads", map[string]any{"filename": filename, "size": size, "contentType": "application/octet-stream"}, 200)
	if err := json.Unmarshal(body, &plan); err != nil {
		b.t.Fatal(err)
	}
	if plan.PartSize != uploadPartSize || len(plan.Parts) != (size+int(uploadPartSize)-1)/int(uploadPartSize) {
		b.t.Fatal("Incorrect chunk plan")
	}
	return plan
}

func (b *browser) transfer(plan uploadPlan, body []byte) {
	b.t.Helper()
	// Upload out of order, as concurrent browser requests can arrive in any order.
	for i := len(plan.Parts) - 1; i >= 0; i-- {
		start := int64(i) * plan.PartSize
		b.raw("PUT", plan.Parts[i].URL, body[start:min(start+plan.PartSize, int64(len(body)))], 200)
	}
}

func TestQueuedUploadsAcceptDuringArchiveAndIsolateConversionFailure(t *testing.T) {
	local := &localStorage{root: t.TempDir()}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &queueFaultStorage{localStorage: local}
	store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if bucket == "private" && key == "catalog.json" && !committed && bytes.Contains(body, []byte(":applying")) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
		return nil
	}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	archive := b.queueCommand("archive", b.state.Catalog.Photos[0].ID)
	b.request("POST", "/api/queue", archive, 202)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("archive did not start")
	}
	b.client.Timeout = 2 * time.Second
	var plans []uploadPlan
	for _, body := range [][]byte{[]byte("invalid media"), smallPNG(t), smallPNG(t)} {
		plan := b.planUpload("queued.png", len(body))
		b.transfer(plan, body)
		plans = append(plans, plan)
		b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
		b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
		// Expiry belongs to the transfer, not its accepted processing job.
		value, _ := local.Read(t.Context(), "private", "uploads/"+plan.Upload.ID+".json")
		var expired upload
		json.Unmarshal(value.Body, &expired)
		expired.Expires = 1
		value.Body, _ = encode(expired)
		if err := local.Write(t.Context(), "private", "uploads/"+expired.ID+".json", value.Body, "application/json", "no-store", nil); err != nil {
			t.Fatal(err)
		}
	}
	b.request("POST", "/api/queue/"+plans[2].Upload.RequestID+"/cancel", nil, 202)
	b.request("DELETE", "/api/uploads/"+plans[1].Upload.ID, nil, 423)
	close(release)
	b.waitQueue(plans[0].Upload.RequestID, "rejected")
	b.waitQueue(plans[1].Upload.RequestID, "succeeded")
	b.waitQueue(plans[2].Upload.RequestID, "cancelled")
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Photos) != 2 || !b.state.Catalog.Photos[0].Archived || b.state.Catalog.Photos[1].ID != plans[1].Upload.ID || b.state.Catalog.Photos[1].Published {
		t.Fatalf("wrong queued effects: %+v", b.state.Catalog.Photos)
	}
	for _, plan := range plans {
		original, err := local.Read(t.Context(), "private", "originals/"+plan.Upload.ID)
		if err != nil || original == nil {
			t.Fatal("accepted/cancelled original was discarded")
		}
		b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
	}
	b.request("DELETE", "/api/uploads/"+plans[1].Upload.ID, nil, 409)
	b.request("POST", "/api/queue/"+plans[0].Upload.RequestID+"/retry", nil, 202)
	b.waitQueue(plans[0].Upload.RequestID, "rejected")
	if objects, err := local.List(t.Context(), "public", "photos/"); err != nil || len(objects) != 0 {
		t.Fatal("processing published media")
	}
}

func TestQueuedUploadRestartsAtDurableBoundaries(t *testing.T) {
	for _, point := range []string{"prepared", "derivative-before", "derivative-after", "catalog-before", "catalog-after"} {
		t.Run(point, func(t *testing.T) {
			local := &localStorage{root: t.TempDir()}
			started := make(chan struct{}, 1)
			var lateBody []byte
			var lateKey string
			store := &queueFaultStorage{localStorage: local}
			store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
				match := point == "prepared" && bucket == "sqlite" && committed && bytes.Contains(body, []byte(`"digests"`))
				match = match || (strings.HasPrefix(point, "derivative-") && strings.HasSuffix(key, "/640.jpg") && committed == (point == "derivative-after"))
				match = match || (strings.HasPrefix(point, "catalog-") && key == "catalog.json" && committed == (point == "catalog-after"))
				if match {
					if point == "derivative-before" {
						lateBody, lateKey = bytes.Clone(body), key
					}
					select {
					case started <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			b := newBrowser(t, store)
			body := smallPNG(t)
			plan := b.planUpload("restart.png", len(body))
			b.transfer(plan, body)
			b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("processing did not reach interruption")
			}
			b.stop()
			restarted := newBrowser(t, local)
			restarted.waitQueue(plan.Upload.RequestID, "succeeded")
			restarted.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
			restarted.request("GET", "/api/catalog", nil, 200)
			if len(restarted.state.Catalog.Photos) != 1 || restarted.state.Catalog.Revision != 1 || restarted.state.Catalog.Photos[0].ID != plan.Upload.ID || restarted.state.Catalog.Photos[0].Width != 53 || restarted.state.Catalog.Photos[0].Height != 29 {
				t.Fatalf("restart duplicated or corrupted the photo: %+v", restarted.state.Catalog)
			}
			if lateBody != nil {
				expected := ""
				if err := local.Write(t.Context(), "private", lateKey, lateBody, "image/jpeg", "no-store", &expected); !errors.Is(err, errConflict) {
					t.Fatalf("late derivative could overwrite completed output: %v", err)
				}
			}
			for _, key := range restarted.state.Catalog.Photos[0].files() {
				value, err := local.Read(t.Context(), "private", key)
				if err != nil || value == nil {
					t.Fatalf("missing %s", key)
				}
			}
		})
	}
}

func TestQueuedUploadRecoversCompletedOriginalBeforeAcceptance(t *testing.T) {
	local := &localStorage{root: t.TempDir()}
	store := &queueFaultStorage{localStorage: local}
	store.fault = func(ctx context.Context, bucket, key string, body []byte, committed bool) error {
		if strings.HasPrefix(key, "uploads/") && !committed && bytes.Contains(body, []byte(`"completed": true`)) {
			return errors.New("lost completion response")
		}
		return nil
	}
	b := newBrowser(t, store)
	body := smallPNG(t)
	plan := b.planUpload("gap.png", len(body))
	b.transfer(plan, body)
	b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 500)
	q, err := b.queue.Load(t.Context())
	if err != nil || len(q.Jobs) != 0 {
		t.Fatal("accepted before verifying original")
	}
	value, _ := local.Read(t.Context(), "private", "uploads/"+plan.Upload.ID+".json")
	var expired upload
	json.Unmarshal(value.Body, &expired)
	expired.Expires = 1
	value.Body, _ = encode(expired)
	if err := local.Write(t.Context(), "private", "uploads/"+expired.ID+".json", value.Body, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	b.stop()
	restarted := newBrowser(t, local)
	restarted.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
	restarted.waitQueue(plan.Upload.RequestID, "succeeded")
	restarted.request("GET", "/api/catalog", nil, 200)
	if len(restarted.state.Catalog.Photos) != 1 {
		t.Fatal("completed original was not recovered")
	}
}

func TestQueuedVideoProducesPrivatePlaybackAndPosters(t *testing.T) {
	path := videoFixture(t, "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=12", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mp4")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local := &localStorage{root: t.TempDir()}
	b := newBrowser(t, local)
	plan := b.planUpload("queued.mov", len(body))
	b.transfer(plan, body)
	b.request("POST", "/api/queue/uploads/"+plan.Upload.ID, nil, 202)
	finished := b.waitQueue(plan.Upload.RequestID, "succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Photos) != 1 {
		t.Fatal("missing processed video")
	}
	p := b.state.Catalog.Photos[0]
	if p.ID != plan.Upload.ID || p.Kind != "video" || p.Width != 96 || p.Height != 64 || p.Duration < .9 || p.Published || p.BlurHash == "" {
		t.Fatalf("incorrect private video: %+v", p)
	}
	for _, key := range p.files() {
		value, err := local.Read(t.Context(), "private", key)
		if err != nil || value == nil {
			t.Fatalf("missing video output %s", key)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(value.Body))
		if digest != finished.Upload.Digests[key] {
			t.Fatalf("unverified output %s", key)
		}
		if public, err := local.Read(t.Context(), "public", key); err != nil || public != nil {
			t.Fatal("video was published automatically")
		}
	}
}

func TestMultipartImagesPreserveOriginalAndConcurrentEdits(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	body := append(smallPNG(t), bytes.Repeat([]byte{0x53, 0x28, 0xf1}, int(uploadPartSize)/3+11)...)
	plan := b.planUpload("Camera image.PNG", len(body))
	b.transfer(plan, body)
	// A failed retry must not replace a previously uploaded complete part.
	b.raw("PUT", plan.Parts[0].URL, []byte("short"), 400)
	b.request("POST", "/api/albums", map[string]string{"title": "Created during upload"}, 200)
	b.async("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil)
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Photos) != 1 || len(b.state.Catalog.Albums) != 1 || b.state.Catalog.Albums[0].Title != "Created during upload" {
		t.Fatal("Upload lost a concurrent catalog edit")
	}
	p := b.state.Catalog.Photos[0]
	if p.ID != plan.Upload.ID || p.Published || p.Kind != "" || p.Width != 53 || p.Height != 29 {
		t.Fatalf("Invalid uploaded draft: %+v", p)
	}
	placeholder := decodePhotoBlurHash(t, p.BlurHash)
	background := color.RGBAModel.Convert(placeholder.At(4, 4)).(color.RGBA)
	if background.R < 240 || background.G < 240 || background.B < 240 {
		t.Fatalf("Transparent PNG BlurHash was not flattened onto white: %+v", background)
	}
	original, err := store.Read(t.Context(), "private", "originals/"+p.ID)
	if err != nil || original == nil || !bytes.Equal(original.Body, body) {
		t.Fatal("Chunk upload changed original bytes")
	}
	for _, key := range p.files() {
		if value, err := store.Read(t.Context(), "private", key); err != nil || value == nil {
			t.Fatalf("Missing derivative %s", key)
		}
		if value, err := store.Read(t.Context(), "public", key); err != nil || value != nil {
			t.Fatal("Unpublished upload escaped into public storage")
		}
	}
	revision := b.state.Catalog.Revision
	b.request("POST", "/api/uploads/"+p.ID+"/complete", nil, 200)
	if len(b.state.Catalog.Photos) != 1 || b.state.Catalog.Revision != revision {
		t.Fatal("Repeated completion duplicated media")
	}
	b.request("DELETE", "/api/uploads/"+p.ID, nil, 409)
	b.request("GET", "/api/catalog", nil, 200)
	if len(b.state.Catalog.Photos) != 1 {
		t.Fatal("Upload cancellation deleted committed media")
	}
}

func TestMultipartRejectsMissingAndExpiredPartsAndCancelsSafely(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for _, input := range []map[string]any{{"filename": "movie.mp4", "size": (512 << 20) + 1}, {"filename": "photo.png", "size": (30 << 20) + 1}, {"filename": "file.exe", "size": 32}, {"filename": "empty.mov", "size": 0}} {
		b.request("POST", "/api/uploads", input, 400)
	}
	plan := b.planUpload("sample.png", len(smallPNG(t)))
	b.request("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil, 400)
	b.raw("PUT", "/api/uploads/"+plan.Upload.ID+"/parts/0", []byte("x"), 400)
	b.raw("PUT", "/api/uploads/"+plan.Upload.ID+"/parts/2", []byte("x"), 400)
	b.transfer(plan, smallPNG(t))
	var job upload
	value, _ := store.Read(t.Context(), "private", "uploads/"+plan.Upload.ID+".json")
	json.Unmarshal(value.Body, &job)
	job.Expires = 1
	value.Body, _ = encode(job)
	store.Write(t.Context(), "private", "uploads/"+job.ID+".json", value.Body, "application/json", "no-store", nil)
	b.raw("PUT", plan.Parts[0].URL, smallPNG(t), 410)
	b.request("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil, 410)
	b.request("DELETE", "/api/uploads/"+plan.Upload.ID, nil, 200)
	keys, err := store.List(t.Context(), "private", "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("Cancelled upload left objects: %v %v", keys, err)
	}
	b.request("POST", "/api/logout", nil, 200)
	b.request("POST", "/api/uploads", map[string]any{"filename": "sample.png", "size": 10}, 401)
	b.raw("PUT", plan.Parts[0].URL, smallPNG(t), 401)
}

func TestUploadedVideoPublishesSeeksArchivesAndDeletes(t *testing.T) {
	path := videoFixture(t, "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=12", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mp4")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	plan := b.planUpload("Camera.MOV", len(body))
	b.transfer(plan, body)
	b.async("POST", "/api/uploads/"+plan.Upload.ID+"/complete", nil)
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	p := b.state.Catalog.Photos[0]
	if p.Kind != "video" || p.Duration < .9 || p.Width != 96 || p.Height != 64 || b.state.Catalog.Version != 4 {
		t.Fatalf("Bad processed video: %+v", p)
	}
	decodePhotoBlurHash(t, p.BlurHash)
	poster, err := store.Read(t.Context(), "private", "photos/"+p.ID+"/640.jpg")
	if err != nil || poster == nil || previewBlurHash(poster.Body) != p.BlurHash {
		t.Fatal("Video BlurHash does not describe its generated poster")
	}
	publicOnly := &app{store: store}
	if _, _, err := publicOnly.load(t.Context(), "private", "catalog.json"); err != nil {
		t.Fatalf("Public-only video library incorrectly requires protected storage: %v", err)
	}
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Private video"}, 200)
	album := b.state.Catalog.Albums[0]
	b.request("PUT", "/api/albums/"+album.ID, map[string]any{"title": album.Title, "slug": album.Slug, "access": "link", "published": true, "photoIds": []string{p.ID}}, 200)
	if b.state.Catalog.Version != 4 {
		t.Fatal("A link album downgraded the video catalog")
	}
	if _, _, err := publicOnly.load(t.Context(), "private", "catalog.json"); err == nil {
		t.Fatal("Video schema allowed protected albums without protected storage")
	}
	b.publish("succeeded")
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 1 || !strings.HasSuffix(live.Photos[0].Src, "/video.mp4") || live.Photos[0].Kind != "video" || live.Photos[0].Duration < .9 {
		t.Fatal("Public projection lost video playback metadata")
	}
	protected, _, err := (&app{store: store, config: configuration{PrivateAlbums: true}}).loadProtected(t.Context(), live)
	if err != nil || len(protected.Photos) != 1 || protected.Photos[0].Kind != "video" {
		t.Fatal("Private projection lost shared video")
	}
	if live.Photos[0].BlurHash != p.BlurHash || protected.Photos[0].BlurHash != p.BlurHash {
		t.Fatal("Public/protected manifests lost video poster BlurHash")
	}
	encoded, err := store.Read(t.Context(), "private", "photos/"+p.ID+"/video.mp4")
	if err != nil || encoded == nil {
		t.Fatal("Missing optimized playback file")
	}
	for _, test := range []struct {
		method, rangeHeader string
		status              int
		start, end          int
	}{{"GET", "", 200, 0, len(encoded.Body)}, {"GET", "bytes=17-42", 206, 17, 43}, {"GET", "bytes=-21", 206, len(encoded.Body) - 21, len(encoded.Body)}, {"GET", "bytes=29-", 206, 29, len(encoded.Body)}, {"HEAD", "bytes=17-42", 206, 17, 43}, {"GET", "bytes=999999999-", 416, 0, 0}, {"GET", "bytes=0-2,5-9", 416, 0, 0}} {
		req, _ := http.NewRequest(test.method, b.server.URL+"/api/photos/"+p.ID+"/video", nil)
		if test.rangeHeader != "" {
			req.Header.Set("Range", test.rangeHeader)
		}
		response, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: %d", test.method, test.rangeHeader, response.StatusCode)
		}
		if test.status != 416 && (response.Header.Get("Content-Type") != "video/mp4" || response.Header.Get("Accept-Ranges") != "bytes") {
			t.Fatal("Video headers missing")
		}
		if test.status == 206 && response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", test.start, test.end-1, len(encoded.Body)) {
			t.Fatal("Incorrect seek range")
		}
		if test.method == "GET" && test.status != 416 && !bytes.Equal(got, encoded.Body[test.start:test.end]) {
			t.Fatal("Seeking returned the wrong bytes")
		}
		if test.method == "HEAD" && len(got) != 0 {
			t.Fatal("HEAD returned video bytes")
		}
	}
	b.request("POST", "/api/photos/"+p.ID+"/archive", nil, 200)
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 0 {
		t.Fatal("Archiving kept the video live")
	}
	b.request("DELETE", "/api/photos/"+p.ID, nil, 200)
	for _, bucket := range []string{"private", "public", "protected"} {
		for _, key := range p.files() {
			value, err := store.Read(t.Context(), bucket, key)
			if err != nil || value != nil {
				t.Fatalf("Video deletion left %s/%s", bucket, key)
			}
		}
	}
}

func TestR2MultipartSignaturesAndAuthoritativePartListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path != "/originals/originals/0123456789abcdef0123456789abcdef" {
			t.Errorf("Unexpected object scope: %s", r.URL)
		}
		switch r.Method {
		case "POST":
			if r.URL.Query().Has("uploads") {
				fmt.Fprint(w, "<InitiateMultipartUploadResult><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>")
			} else {
				body, _ := io.ReadAll(r.Body)
				if !bytes.Contains(body, []byte("trusted-etag")) {
					t.Error("Completion did not use service ETags")
				}
				fmt.Fprint(w, "<CompleteMultipartUploadResult><ETag>completed</ETag></CompleteMultipartUploadResult>")
			}
		case "GET":
			fmt.Fprint(w, "<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><ETag>trusted-etag</ETag><Size>53</Size></Part></ListPartsResult>")
		case "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("Unexpected control request: %s", r.Method)
		}
	}))
	defer server.Close()
	store := newR2Storage(server.URL, "dummy-key", "dummy-secret", "originals", "gallery")
	key := "originals/0123456789abcdef0123456789abcdef"
	id, err := store.BeginUpload(t.Context(), key)
	if err != nil || id != "fixture-upload" {
		t.Fatalf("Begin multipart: %s %v", id, err)
	}
	signed, err := store.PartURL(t.Context(), key, id, 1, 53)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != strings.TrimPrefix(server.URL, "http://") || u.Path != "/originals/"+key || q.Get("partNumber") != "1" || q.Get("uploadId") != id || q.Get("X-Amz-Expires") != "3600" || len(q.Get("X-Amz-Signature")) != 64 || !strings.Contains(q.Get("X-Amz-SignedHeaders"), "content-length") || strings.Contains(signed, "dummy-secret") {
		t.Fatalf("Incorrect signed upload scope: %s", signed)
	}
	parts, err := store.ListUploadParts(t.Context(), key, id)
	if err != nil || len(parts) != 1 || parts[0].Size != 53 || parts[0].ETag != "trusted-etag" {
		t.Fatalf("Untrusted/incomplete part inventory: %+v %v", parts, err)
	}
	if err := store.FinishUpload(t.Context(), key, id, parts); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortUpload(t.Context(), key, id); err != nil {
		t.Fatal(err)
	}
}
