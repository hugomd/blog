package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"reflect"
	"time"
)

type publication struct {
	ID          string `json:"id"`
	Revision    int    `json:"revision"`
	CatalogETag string `json:"catalogETag"`
	State       string `json:"state"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	Error       string `json:"error,omitempty"`
}

type publicationCounts struct {
	Live      int `json:"live"`
	Next      int `json:"next"`
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

type publicationChanges struct {
	Changed         bool              `json:"changed"`
	Photos          publicationCounts `json:"photos"`
	Albums          publicationCounts `json:"albums"`
	ProtectedPhotos publicationCounts `json:"protectedPhotos"`
	ProtectedAlbums publicationCounts `json:"protectedAlbums"`
}

// Compare the public projection, not private revisions or publication settings.
func (next catalog) changesFrom(live catalog) publicationChanges {
	changes := publicationChanges{
		Photos: publicationCounts{Live: len(live.Photos), Next: len(next.Photos)},
		Albums: publicationCounts{Live: len(live.Albums), Next: len(next.Albums)},
	}
	photos := map[string]photo{}
	for _, p := range live.Photos {
		// BlurHash is derived, optional metadata. Legacy drafts deliberately
		// need not be rewritten when publication backfills their live hashes.
		p.BlurHash = ""
		photos[p.ID] = p
	}
	samePhotoOrder := len(live.Photos) == len(next.Photos)
	for i, p := range next.Photos {
		p.BlurHash = ""
		if i >= len(live.Photos) || live.Photos[i].ID != p.ID {
			samePhotoOrder = false
		}
		if previous, ok := photos[p.ID]; !ok {
			changes.Photos.Added++
		} else if !reflect.DeepEqual(previous, p) {
			changes.Photos.Updated++
		} else {
			changes.Photos.Unchanged++
		}
	}
	changes.Photos.Removed = changes.Photos.Live - changes.Photos.Updated - changes.Photos.Unchanged
	albums := map[string]album{}
	for _, item := range live.Albums {
		albums[item.ID] = item
	}
	sameAlbumOrder := len(live.Albums) == len(next.Albums)
	for i, item := range next.Albums {
		if i >= len(live.Albums) || live.Albums[i].ID != item.ID {
			sameAlbumOrder = false
		}
		if previous, ok := albums[item.ID]; !ok {
			changes.Albums.Added++
		} else if !reflect.DeepEqual(previous, item) {
			changes.Albums.Updated++
		} else {
			changes.Albums.Unchanged++
		}
	}
	changes.Albums.Removed = changes.Albums.Live - changes.Albums.Updated - changes.Albums.Unchanged
	changes.Changed = !samePhotoOrder || !sameAlbumOrder || changes.Photos.Unchanged != changes.Photos.Next || changes.Albums.Unchanged != changes.Albums.Next
	return changes
}

func (a *app) publicationStatus() publication {
	a.publicationMu.RLock()
	defer a.publicationMu.RUnlock()
	job := a.publication
	if job.State == "" {
		job.State = "idle"
	}
	return job
}

func (a *app) setPublication(job publication) {
	a.publicationMu.Lock()
	a.publication = job
	a.publicationMu.Unlock()
}

func (a *app) savePublication(ctx context.Context, job publication) error {
	body, err := encode(job)
	if err != nil {
		return err
	}
	return a.store.Write(ctx, "private", "publication.json", body, "application/json", "no-store", nil)
}

// Fail promptly rather than leaving mutation requests queued behind a long copy.
func (a *app) lockCatalog() error {
	a.queueMu.Lock()
	if a.queueBlockedLocked() {
		a.queueMu.Unlock()
		return &requestError{423, "Finish or resolve queued actions before making this change."}
	}
	if a.publicationStatus().State == "running" || !a.mu.TryLock() {
		a.queueMu.Unlock()
		return &requestError{423, "Another change is in progress. Wait and retry."}
	}
	a.queueMu.Unlock()
	if err := a.finishPublicationWrite(a.background); err != nil {
		a.mu.Unlock()
		return err
	}
	return nil
}

func (a *app) startPublication(w http.ResponseWriter, r *http.Request) error {
	job := a.publicationStatus()
	if job.State == "running" && job.CatalogETag == r.Header.Get("If-Match") {
		respond(w, http.StatusAccepted, map[string]any{"publication": job})
		return nil
	}
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	c, etag, err := a.load(r.Context(), "private", "catalog.json")
	if err != nil {
		return err
	}
	if err := checkETag(r, etag); err != nil {
		return err
	}
	if r.URL.Query().Get("republish") == "1" {
		live, liveETag, err := a.load(r.Context(), "public", "index.json")
		if err != nil {
			return err
		}
		protected, _, err := a.loadProtected(r.Context(), live)
		if err != nil {
			return err
		}
		if liveETag == "" || c.publicRelease(a.config.PublicURL).changesFrom(live).Changed || c.protectedRelease().changesFrom(protected).Changed {
			return &requestError{409, "Republish requires an existing publication with no pending publication changes. Publish or undo saved changes first."}
		}
	}
	for _, item := range c.Albums {
		if item.Access == "password" && (!a.config.PrivateAlbums || item.PasswordHash == "" || item.AccessVersion == "") {
			return badRequest("Configure protected storage and an album password before publishing.")
		}
		if item.Access == "link" && (!a.config.PrivateAlbums || item.LinkHash == "" || item.LinkToken == "" || item.AccessVersion == "") {
			return badRequest("Configure protected storage and a private share link before publishing.")
		}
	}
	if etag == "" {
		etag = "new"
	}
	job = publication{ID: identifier(), Revision: c.Revision, CatalogETag: etag, State: "running"}
	if err := a.savePublication(r.Context(), job); err != nil {
		return err
	}
	a.setPublication(job)
	a.launchPublication()
	respond(w, http.StatusAccepted, map[string]any{"publication": job})
	return nil
}

// The private job record survives a pod replacement; never depend on the browser.
func (a *app) resumePublication() error {
	value, err := a.store.Read(a.background, "private", "publication.json")
	if err != nil || value == nil {
		return err
	}
	var job publication
	if err := json.Unmarshal(value.Body, &job); err != nil {
		return err
	}
	a.setPublication(job)
	if job.State == "running" {
		a.launchPublication()
	}
	return nil
}

func (a *app) launchPublication() {
	a.workers.Go(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		ctx, cancel := context.WithTimeout(a.background, 30*time.Minute)
		defer cancel()
		job := a.publicationStatus()
		c, etag, err := a.load(ctx, "private", "catalog.json")
		if etag == "" {
			etag = "new"
		}
		if err == nil && (etag != job.CatalogETag || c.Revision != job.Revision) {
			err = errConflict
		}
		a.queueMu.Lock()
		blocked := a.queueBlockedLocked()
		a.queueMu.Unlock()
		if blocked {
			err = errConflict
		}
		if err == nil {
			err = a.publish(ctx, c, func(completed, total int) {
				a.publicationMu.Lock()
				a.publication.Completed, a.publication.Total = completed, total
				a.publicationMu.Unlock()
			})
		}
		if a.background.Err() != nil {
			// Leave the durable running marker for the next process to resume.
			return
		}
		job = a.publicationStatus()
		job.State = "succeeded"
		if err != nil {
			job.State = "failed"
			job.Error = "Publication failed. Reload the library and retry; the previous live index is retained unless its final write completed."
			if errors.Is(err, errConflict) {
				job.Error = "The library or live index changed. Reload before publishing again."
			}
			log.Printf("Publication failed: %T", err)
		}
		if err := a.savePublication(ctx, job); err != nil {
			// A completed index remains valid even if this status write fails.
			log.Printf("Cannot save publication outcome: %T", err)
		}
		a.setPublication(job)
	})
}

// Persist the exact conditional request before sending it to R2. A lost response
// leaves this record intact. Replaying it establishes the object (and invalidates
// the old precondition) before an archive can scan history or report completion.
// These are generated manifests, not incoming HTTP requests or user passwords.
type publicationWrite struct {
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Body     []byte `json:"body"`
	Expected string `json:"expected"`
}

func (a *app) writePublicationManifest(ctx context.Context, bucket, key string, body []byte, expected string) error {
	a.queueMu.Lock()
	next := a.queue
	if next.PublicationWrite != nil || a.queueError != "" {
		a.queueMu.Unlock()
		return errors.New("resolve the previous publication write before continuing")
	}
	next.PublicationWrite = &publicationWrite{Bucket: bucket, Key: key, Body: bytes.Clone(body), Expected: expected}
	err := a.saveQueueLocked(ctx, next)
	a.queueMu.Unlock()
	if err != nil {
		return err
	}
	return a.finishPublicationWrite(ctx)
}

// Call only while holding the catalog writer lock (or before serving requests).
func (a *app) finishPublicationWrite(ctx context.Context) error {
	a.queueMu.Lock()
	intent, uncertain := a.queue.PublicationWrite, a.queueError != ""
	a.queueMu.Unlock()
	if uncertain {
		return errors.New("recover queue storage before reconciling publication")
	}
	if intent == nil {
		return nil
	}
	cache := "public, max-age=30, must-revalidate"
	if intent.Bucket == "protected" {
		cache = "private, no-store"
	}
	contentType := "application/json"
	if intent.Key == "index.xml" {
		contentType = "application/rss+xml; charset=utf-8"
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err := a.store.Write(writeCtx, intent.Bucket, intent.Key, intent.Body, contentType, cache, &intent.Expected)
	cancel()
	var conflict error
	if err != nil {
		readCtx, cancel := context.WithTimeout(ctx, time.Minute)
		current, readErr := a.store.Read(readCtx, intent.Bucket, intent.Key)
		cancel()
		if readErr != nil {
			return readErr
		}
		if current == nil {
			return err
		}
		if !bytes.Equal(current.Body, intent.Body) {
			if current.ETag == intent.Expected {
				return err
			}
			// A newer object fences our request. Never overwrite it; discard
			// only this obsolete intent and report conflict to the publisher.
			conflict = errConflict
		}
	}
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	next := a.queue
	next.PublicationWrite = nil
	if err := a.saveQueueLocked(ctx, next); err != nil {
		return err
	}
	return conflict
}
