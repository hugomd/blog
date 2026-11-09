package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type photo struct {
	ID           string            `json:"id"`
	Filename     string            `json:"filename,omitempty"`
	Alt          string            `json:"alt"`
	Caption      string            `json:"caption"`
	Tags         []string          `json:"tags"`
	Published    bool              `json:"published"`
	Archived     bool              `json:"archived,omitempty"`
	Archiving    bool              `json:"archiving,omitempty"`
	Deleting     bool              `json:"deleting,omitempty"`
	UploadedAt   string            `json:"uploadedAt"`
	CapturedDate string            `json:"capturedDate"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Exif         map[string]string `json:"exif"`
	Thumbnail    string            `json:"thumbnail,omitempty"`
	Src          string            `json:"src,omitempty"`
}

type album struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Published   bool     `json:"published"`
	PhotoIDs    []string `json:"photoIds"`
	CoverID     string   `json:"coverId"`
}

type catalog struct {
	Version     int     `json:"version"`
	Revision    int     `json:"revision"`
	Photos      []photo `json:"photos"`
	Albums      []album `json:"albums"`
	PublishedAt string  `json:"publishedAt,omitempty"`
}

func identifier() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

func encode(value any) ([]byte, error) { return json.MarshalIndent(value, "", "  ") }

func (a *app) load(ctx context.Context, bucket, key string) (catalog, string, error) {
	value, err := a.store.Read(ctx, bucket, key)
	result := catalog{Version: 1, Photos: []photo{}, Albums: []album{}}
	if err != nil || value == nil {
		return result, "", err
	}
	if err := json.Unmarshal(value.Body, &result); err != nil {
		return catalog{}, "", fmt.Errorf("invalid catalog: %w", err)
	}
	if result.Version != 1 {
		return catalog{}, "", errors.New("unsupported catalog version")
	}
	return result, value.ETag, nil
}

func (a *app) save(ctx context.Context, c *catalog, etag string) (string, error) {
	c.Revision++
	body, err := encode(c)
	if err != nil {
		return "", err
	}
	if err := a.store.Write(ctx, "private", "catalog.json", body, "application/json", "no-store", &etag); err != nil {
		return "", err
	}
	// Use the storage service's ETag, not a locally assumed digest format.
	value, err := a.store.Read(ctx, "private", "catalog.json")
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", errors.New("saved catalog not found")
	}
	if !bytes.Equal(value.Body, body) {
		return "", errConflict
	}
	return value.ETag, nil
}

func (c catalog) photoReady(p photo) bool {
	if p.Deleting || p.Archived || p.Archiving {
		return false
	}
	if p.Published {
		return true
	}
	for _, item := range c.Albums {
		if item.Published && contains(item.PhotoIDs, p.ID) {
			return true
		}
	}
	return false
}

func (a *app) publish(ctx context.Context, c catalog, progress func(int, int)) error {
	previous, etag, err := a.load(ctx, "public", "index.json")
	if err != nil {
		return err
	}
	alreadyPublished := map[string]bool{}
	for _, p := range previous.Photos {
		alreadyPublished[p.ID] = true
	}
	release := catalog{Version: 1, Revision: c.Revision, PublishedAt: now(), Photos: []photo{}, Albums: []album{}}
	published := map[string]bool{}
	keys := []string{}
	for _, p := range c.Photos {
		if !c.photoReady(p) {
			continue
		}
		if p.Alt == "" {
			return badRequest("Add alt text to every published photo.")
		}
		if !alreadyPublished[p.ID] {
			for _, size := range []int{640, 1280, 2560} {
				keys = append(keys, fmt.Sprintf("photos/%s/%d.jpg", p.ID, size))
			}
		}
		p.Published = true
		p.Filename = ""
		p.Thumbnail = strings.TrimRight(a.config.PublicURL, "/") + "/photos/" + p.ID + "/640.jpg"
		p.Src = strings.TrimRight(a.config.PublicURL, "/") + "/photos/" + p.ID + "/2560.jpg"
		release.Photos = append(release.Photos, p)
		published[p.ID] = true
	}
	// Include standalone and published-album photos once, newest first.
	sort.Slice(release.Photos, func(i, j int) bool { return release.Photos[i].UploadedAt > release.Photos[j].UploadedAt })
	for _, item := range c.Albums {
		if !item.Published {
			continue
		}
		originalIDs := item.PhotoIDs
		item.PhotoIDs = []string{}
		for _, id := range originalIDs {
			if published[id] {
				item.PhotoIDs = append(item.PhotoIDs, id)
			}
		}
		if !contains(item.PhotoIDs, item.CoverID) {
			item.CoverID = ""
			if len(item.PhotoIDs) > 0 {
				item.CoverID = item.PhotoIDs[0]
			}
		}
		release.Albums = append(release.Albums, item)
	}
	// Copy within R2, with a bounded number of requests and no image bytes in Go.
	// Finish every copy before changing the discoverable public manifest.
	copyContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string, len(keys))
	for _, key := range keys {
		jobs <- key
	}
	close(jobs)
	progress(0, len(keys))
	var workers sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	failures := make(chan error, 1)
	for range min(4, len(keys)) {
		workers.Go(func() {
			for key := range jobs {
				if copyContext.Err() != nil {
					return
				}
				if err := a.store.Copy(copyContext, "private", "public", key, "image/jpeg", "public, max-age=31536000, immutable"); err != nil {
					select {
					case failures <- err:
					default:
					}
					cancel()
					return
				}
				progressMu.Lock()
				completed++
				progress(completed, len(keys))
				progressMu.Unlock()
			}
		})
	}
	workers.Wait()
	select {
	case err := <-failures:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := encode(release)
	if err != nil {
		return err
	}
	// Historical snapshots can be redacted by archiving; do not mark immutable.
	if err := a.store.Write(ctx, "public", "releases/"+identifier()+".json", body, "application/json", "public, max-age=30, must-revalidate", nil); err != nil {
		return err
	}
	return a.store.Write(ctx, "public", "index.json", body, "application/json", "public, max-age=30, must-revalidate", &etag)
}

func (c *catalog) removePhoto(id string) {
	photos := []photo{}
	for _, p := range c.Photos {
		if p.ID != id {
			photos = append(photos, p)
		}
	}
	c.Photos = photos
	for i := range c.Albums {
		item := &c.Albums[i]
		ids := []string{}
		for _, existing := range item.PhotoIDs {
			if existing != id {
				ids = append(ids, existing)
			}
		}
		item.PhotoIDs = ids
		if item.CoverID == id {
			item.CoverID = ""
			if len(ids) > 0 {
				item.CoverID = ids[0]
			}
		}
	}
}

func contains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}
