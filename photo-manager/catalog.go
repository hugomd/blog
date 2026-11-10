package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Kind         string            `json:"kind,omitempty"`
	Duration     float64           `json:"duration,omitempty"`
	Exif         map[string]string `json:"exif"`
	Thumbnail    string            `json:"thumbnail,omitempty"`
	Src          string            `json:"src,omitempty"`
	BlurHash     string            `json:"blurhash,omitempty"`
}

func (p photo) files() []string {
	keys := []string{fmt.Sprintf("photos/%s/640.jpg", p.ID), fmt.Sprintf("photos/%s/1280.jpg", p.ID), fmt.Sprintf("photos/%s/2560.jpg", p.ID)}
	if p.Kind == "video" {
		keys = append(keys, "photos/"+p.ID+"/video.mp4")
	}
	return keys
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
	LinkToken     string   `json:"linkToken,omitempty"`
	LinkHash      string   `json:"linkHash,omitempty"`
	PasswordSet   bool     `json:"passwordSet,omitempty"`
}

func (a album) protected() bool { return a.Access == "password" || a.Access == "link" }

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
	if result.Version < 1 || result.Version > 4 {
		return catalog{}, "", errors.New("unsupported catalog version")
	}
	for _, p := range result.Photos {
		if p.Kind != "" && p.Kind != "video" {
			return catalog{}, "", errors.New("unsupported media kind")
		}
	}
	protected := false
	for _, item := range result.Albums {
		if item.Access != "" && item.Access != "public" && item.Access != "password" && item.Access != "link" {
			return catalog{}, "", errors.New("unsupported album access")
		}
		protected = protected || item.protected()
	}
	if !a.config.PrivateAlbums && ((bucket == "private" && (result.Version == 2 || result.Version == 3 || protected)) || result.ProtectedRelease != "") {
		return catalog{}, "", &requestError{503, "Restore protected album storage configuration before editing this library."}
	}
	return result, value.ETag, nil
}

func (a *app) save(ctx context.Context, c *catalog, etag string) (string, error) {
	for _, p := range c.Photos {
		if p.Kind == "video" {
			c.Version = 4 // Old publishers must not omit video files or delete only posters.
		}
	}
	for _, item := range c.Albums {
		// Older publishers must reject modes they would otherwise treat as public.
		if item.Access == "link" {
			c.Version = max(c.Version, 3)
		} else if item.Access == "password" {
			c.Version = max(c.Version, 2)
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
		if item.Published && item.protected() == protected {
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
			if p.Kind == "video" {
				p.Src = strings.TrimRight(publicURL, "/") + "/photos/" + p.ID + "/video.mp4"
			}
		}
		release.Photos = append(release.Photos, p)
		published[p.ID] = true
	}
	// Include standalone and published-album photos once, newest first.
	sort.Slice(release.Photos, func(i, j int) bool { return release.Photos[i].UploadedAt > release.Photos[j].UploadedAt })
	for _, item := range c.Albums {
		if !item.Published || item.protected() != protected {
			continue
		}
		// Explicit projection keeps private credentials out of public snapshots.
		next := album{ID: item.ID, Title: item.Title, Slug: item.Slug, Description: item.Description, Published: true, PhotoIDs: []string{}, CoverID: item.CoverID}
		if protected {
			next.Access = item.Access
			next.AccessVersion = item.AccessVersion
			if item.Access == "password" {
				next.PasswordHash = item.PasswordHash
			} else if item.Access == "link" {
				next.LinkHash = item.LinkHash
			}
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
	// Derive metadata only for the authorized projections, without changing the
	// draft catalog/revision/ETag the durable publication job is pinned to.
	// Reuse live hashes first, including photos moving between access modes.
	hashes := map[string]string{}
	for _, prior := range []catalog{previous, previousProtected} {
		for _, p := range prior.Photos {
			if p.BlurHash != "" {
				hashes[p.ID] = p.BlurHash
			}
		}
	}
	for _, target := range []*catalog{&release, &protected} {
		for i := range target.Photos {
			p := &target.Photos[i]
			if p.BlurHash != "" {
				continue
			}
			hash, known := hashes[p.ID]
			if !known {
				// A 640px JPEG is small; bound reads and decoded dimensions even
				// if a legacy object is corrupt. Missing previews stay neutral.
				if source, err := a.store.Open(ctx, "private", "photos/"+p.ID+"/640.jpg", 0, 0); err == nil {
					body, err := io.ReadAll(io.LimitReader(source, 2<<20))
					source.Close()
					if err == nil {
						hash = previewBlurHash(body)
					}
				}
				hashes[p.ID] = hash
			}
			p.BlurHash = hash
		}
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
			if !alreadyPublished[p.ID] {
				for _, key := range p.files() {
					keys = append(keys, copyJob{target.bucket, key})
				}
			}
		}
	}
	// Copy media within R2 with a bounded number of requests.
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
				contentType := "image/jpeg"
				if strings.HasSuffix(job.key, ".mp4") {
					contentType = "video/mp4"
				}
				if err := a.store.Copy(copyContext, "private", job.bucket, job.key, contentType, cache); err != nil {
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
