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
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Slug          string   `json:"slug"`
	Description   string   `json:"description"`
	Published     bool     `json:"published"`
	PhotoIDs      []string `json:"photoIds"`
	CoverID       string   `json:"coverId"`
	Access        string   `json:"access,omitempty"`
	PasswordHash  string   `json:"passwordHash,omitempty"`
	AccessVersion string   `json:"accessVersion,omitempty"`
	PasswordSet   bool     `json:"passwordSet,omitempty"`
}

type catalog struct {
	Version          int     `json:"version"`
	Revision         int     `json:"revision"`
	Photos           []photo `json:"photos"`
	Albums           []album `json:"albums"`
	PublishedAt      string  `json:"publishedAt,omitempty"`
	ProtectedRelease string  `json:"protectedRelease,omitempty"`
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
	if result.Version != 1 && result.Version != 2 {
		return catalog{}, "", errors.New("unsupported catalog version")
	}
	if !a.config.PrivateAlbums && ((bucket == "private" && result.Version == 2) || result.ProtectedRelease != "") {
		return catalog{}, "", &requestError{503, "Restore protected album storage configuration before editing this library."}
	}
	return result, value.ETag, nil
}

func (a *app) save(ctx context.Context, c *catalog, etag string) (string, error) {
	for _, item := range c.Albums {
		if item.Access == "password" {
			// Older binaries must reject, rather than publicly publish, this catalog.
			c.Version = 2
		}
	}
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

func (c catalog) publicRelease(publicURL string) catalog {
	return c.release(publicURL, false)
}

func (c catalog) protectedRelease() catalog {
	return c.release("", true)
}

func (c catalog) release(publicURL string, protected bool) catalog {
	release := catalog{Version: 1, Revision: c.Revision, Photos: []photo{}, Albums: []album{}}
	members := map[string]bool{}
	for _, item := range c.Albums {
		if item.Published && (item.Access == "password") == protected {
			for _, id := range item.PhotoIDs {
				members[id] = true
			}
		}
	}
	published := map[string]bool{}
	for _, p := range c.Photos {
		if p.Archived || p.Archiving || p.Deleting || (!members[p.ID] && (protected || !p.Published)) {
			continue
		}
		p.Published = true
		p.Filename = ""
		p.Thumbnail, p.Src = "", ""
		if !protected {
			p.Thumbnail = strings.TrimRight(publicURL, "/") + "/photos/" + p.ID + "/640.jpg"
			p.Src = strings.TrimRight(publicURL, "/") + "/photos/" + p.ID + "/2560.jpg"
		}
		release.Photos = append(release.Photos, p)
		published[p.ID] = true
	}
	// Include standalone and published-album photos once, newest first.
	sort.Slice(release.Photos, func(i, j int) bool { return release.Photos[i].UploadedAt > release.Photos[j].UploadedAt })
	for _, item := range c.Albums {
		if !item.Published || (item.Access == "password") != protected {
			continue
		}
		// Explicit projection keeps private credentials out of public snapshots.
		next := album{ID: item.ID, Title: item.Title, Slug: item.Slug, Description: item.Description, Published: true, PhotoIDs: []string{}, CoverID: item.CoverID}
		if protected {
			next.Access, next.PasswordHash, next.AccessVersion = "password", item.PasswordHash, item.AccessVersion
		}
		originalIDs := item.PhotoIDs
		for _, id := range originalIDs {
			if published[id] {
				next.PhotoIDs = append(next.PhotoIDs, id)
			}
		}
		if !contains(next.PhotoIDs, next.CoverID) {
			next.CoverID = ""
			if len(next.PhotoIDs) > 0 {
				next.CoverID = next.PhotoIDs[0]
			}
		}
		release.Albums = append(release.Albums, next)
	}
	return release
}

func (a *app) publish(ctx context.Context, c catalog, progress func(int, int)) error {
	previous, etag, err := a.load(ctx, "public", "index.json")
	if err != nil {
		return err
	}
	previousProtected, _, err := a.loadProtected(ctx, previous)
	if err != nil {
		return err
	}
	release := c.publicRelease(a.config.PublicURL)
	release.PublishedAt = now()
	protected := c.protectedRelease()
	if len(protected.Albums) > 0 && !a.config.PrivateAlbums {
		return badRequest("Configure protected album storage before publishing private albums.")
	}
	type copyJob struct{ bucket, key string }
	keys := []copyJob{}
	for _, target := range []struct {
		bucket         string
		next, previous catalog
	}{{"public", release, previous}, {"protected", protected, previousProtected}} {
		alreadyPublished := map[string]bool{}
		for _, p := range target.previous.Photos {
			alreadyPublished[p.ID] = true
		}
		for _, p := range target.next.Photos {
			if p.Alt == "" {
				return badRequest("Add alt text to every published photo.")
			}
			if !alreadyPublished[p.ID] {
				for _, size := range []int{640, 1280, 2560} {
					keys = append(keys, copyJob{target.bucket, fmt.Sprintf("photos/%s/%d.jpg", p.ID, size)})
				}
			}
		}
	}
	// Copy within R2, with a bounded number of requests and no image bytes in Go.
	// Finish every copy before changing the discoverable public manifest.
	copyContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan copyJob, len(keys))
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
			for job := range jobs {
				if copyContext.Err() != nil {
					return
				}
				cache := "public, max-age=31536000, immutable"
				if job.bucket == "protected" {
					cache = "private, no-store"
				}
				if err := a.store.Copy(copyContext, "private", job.bucket, job.key, "image/jpeg", cache); err != nil {
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
	if a.config.PrivateAlbums {
		// The final public index write commits both projections. Interrupted
		// attempts leave only undiscoverable, private release snapshots.
		release.ProtectedRelease = identifier()
		protected.PublishedAt = release.PublishedAt
		body, err := encode(protected)
		if err != nil {
			return err
		}
		if err := a.store.Write(ctx, "protected", "releases/"+release.ProtectedRelease+".json", body, "application/json", "private, no-store", nil); err != nil {
			return err
		}
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
