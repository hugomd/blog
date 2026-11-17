package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

// Only completed originals enter SQLite. Prepared metadata and content hashes
// pin every derivative before its first PUT; retries cannot replace its bytes.
type queuedUpload struct {
	Source  upload            `json:"source"`
	Photo   *photo            `json:"photo,omitempty"`
	Digests map[string]string `json:"digests,omitempty"`
}

// Called with uploadMu held. Even terminal jobs retain ownership until pruning;
// cancellation uses the queue, never a competing legacy upload endpoint.
func (a *app) checkUploadUnowned(id string) error {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueError != "" {
		return &requestError{503, a.queueError}
	}
	for _, job := range a.queue.Jobs {
		if job.Upload != nil && job.Upload.Source.ID == id {
			return &requestError{409, "This upload belongs to the processing queue. Check its status there."}
		}
	}
	return nil
}

func (a *app) enqueueUpload(w http.ResponseWriter, r *http.Request) error {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueError != "" {
		return &requestError{503, a.queueError}
	}
	id := r.PathValue("id")
	for _, job := range a.queue.Jobs {
		if job.Upload != nil && job.Upload.Source.ID == id {
			w.Header().Set("Location", "/api/queue")
			respond(w, 202, map[string]any{"job": job})
			return nil
		}
	}
	if a.operationStatus().State == "running" || a.publicationStatus().State == "running" {
		return &requestError{423, "Wait for the current edit or publication before queuing processing."}
	}
	if !a.queueBlockedLocked() {
		if !a.mu.TryLock() {
			return &requestError{423, "Another change is finishing. Retry processing shortly."}
		}
		defer a.mu.Unlock()
	}
	next := a.queue
	next.Jobs = []queueJob{}
	pending := 0
	for _, job := range a.queue.Jobs {
		if queuePending(job) {
			pending++
		}
		created, _ := queueRequestTime(job.ID)
		if queuePending(job) || time.Since(created) <= queueRetention {
			next.Jobs = append(next.Jobs, job)
		}
	}
	if pending >= queueLimit || len(next.Jobs) >= queueHistoryLimit {
		return &requestError{429, "The queue is full. Keep this upload and retry processing after jobs finish."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	source, err := a.finalizeUpload(ctx, id)
	if err != nil {
		return err
	}
	created, err := queueRequestTime(source.RequestID)
	if err != nil || time.Since(created) > queueRetention || time.Until(created) > 5*time.Minute {
		return &requestError{409, "This upload's processing request expired. Start a new upload."}
	}
	c, _, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	for _, p := range c.Photos {
		if p.ID == id {
			return &requestError{409, "This upload is already in the library."}
		}
	}
	job := queueJob{ID: source.RequestID, Action: "upload", Targets: []queueTarget{{ID: id}}, State: "queued", CreatedAt: now(), CanCancel: true, Upload: &queuedUpload{Source: source}}
	next.Jobs = append(next.Jobs, job)
	if err := a.saveQueueLocked(ctx, next); err != nil {
		return err
	}
	w.Header().Set("Location", "/api/queue")
	respond(w, 202, map[string]any{"job": job})
	a.startQueueLocked()
	return nil
}

func mediaContentType(key string) string {
	if strings.HasSuffix(key, ".mp4") {
		return "video/mp4"
	}
	return "image/jpeg"
}

func contentDigest(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (a *app) verifyUploadFile(ctx context.Context, key, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	file, err := a.store.Open(ctx, "private", key, 0, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	digest, err := contentDigest(file)
	if err != nil {
		return err
	}
	if digest != expected {
		return errors.New("stored derivative differs from the accepted processing result")
	}
	return nil
}

// runQueue owns the catalog writer lock. The receipt and uploaded photo commit
// together; an uncertain save must be resolved before another job advances.
func (a *app) executeQueuedUpload(ctx context.Context, job *queueJob) error {
	c, etag, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	if c.QueueReceipt == job.ID+":done" {
		return a.cleanupUpload(ctx, job.Upload.Source.ID)
	}
	for _, p := range c.Photos {
		if p.ID == job.Upload.Source.ID {
			return errors.New("upload ID already exists without its queue receipt")
		}
	}
	prepared := job.Upload.Photo != nil
	complete := prepared
	if prepared {
		for key, digest := range job.Upload.Digests {
			if err := a.verifyUploadFile(ctx, key, digest); err != nil {
				complete = false
				break
			}
		}
	}
	if !complete {
		tmp, err := os.MkdirTemp("", "studio-process-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		p, files, err := a.prepareUpload(ctx, job.Upload.Source, tmp)
		if err != nil {
			var conversion *uploadConversionError
			if !prepared && ctx.Err() == nil && errors.As(err, &conversion) {
				return &queueRejected{badRequest("Processing failed before any catalog changes. The original is retained; other jobs can continue.")}
			}
			return err
		}
		digests := map[string]string{}
		for key, path := range files {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			digest, err := contentDigest(file)
			file.Close()
			if err != nil {
				return err
			}
			digests[key] = digest
		}
		if prepared {
			p.UploadedAt = job.Upload.Photo.UploadedAt
			if !reflect.DeepEqual(digests, job.Upload.Digests) || !reflect.DeepEqual(p, *job.Upload.Photo) {
				return errors.New("processing output changed; restore the original encoder before retrying")
			}
		} else {
			next := *job
			next.Upload = &queuedUpload{Source: job.Upload.Source, Photo: &p, Digests: digests}
			a.queueMu.Lock()
			err = a.updateQueueJobLocked(ctx, next)
			a.queueMu.Unlock()
			if err != nil {
				return err
			}
			*job = next
		}
		for key, path := range files {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			expected := ""
			writeCtx, cancel := context.WithTimeout(ctx, time.Minute)
			err = a.store.WriteFile(writeCtx, "private", key, file, mediaContentType(key), "private, no-store", &expected)
			cancel()
			file.Close()
			if err != nil {
				// A conflict or lost acknowledgement is safe only if the bytes
				// already stored match the result persisted before the first PUT.
				if verifyErr := a.verifyUploadFile(ctx, key, digests[key]); verifyErr != nil {
					return err
				}
			}
		}
	}
	c.Version, c.QueueReceipt = 5, job.ID+":done"
	c.Photos = append(c.Photos, *job.Upload.Photo)
	if _, err := a.save(ctx, &c, etag); err != nil {
		return err
	}
	return a.cleanupUpload(ctx, job.Upload.Source.ID)
}
