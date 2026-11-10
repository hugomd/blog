package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed public/*
var assets embed.FS

type configuration struct {
	Origin, PublicURL, AuthMode, Password, AllowedEmail, LocalRoot string
	UploadOrigin                                                   string
	Development                                                    bool
	PrivateAlbums                                                  bool
	GalleryURL                                                     string
}

type app struct {
	store         storage
	config        configuration
	mu            sync.Mutex
	authMu        sync.Mutex
	sessions      map[string]time.Time
	loginAttempts []time.Time
	background    context.Context
	workers       sync.WaitGroup
	publicationMu sync.RWMutex
	publication   publication
	operationMu   sync.RWMutex
	operation     operation
}

type requestError struct {
	status  int
	message string
}

func (e *requestError) Error() string { return e.message }
func badRequest(message string) error { return &requestError{400, message} }
func now() string                     { return time.Now().UTC().Format(time.RFC3339Nano) }

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return badRequest("Invalid request body.")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return badRequest("Send one JSON object.")
	}
	return nil
}

func (a *app) handler(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/api/login" && r.URL.Path != "/api/logout" && r.Context().Value(operationContext{}) != true && a.operationStatus().State == "running" {
			respond(w, 423, map[string]string{"error": "Another change is in progress. Wait and retry."})
			return
		}
		if err := fn(w, r); err != nil {
			status, message := 500, "Storage or processing failed. Reload to check the current state before retrying."
			var clientError *requestError
			var sizeError *http.MaxBytesError
			switch {
			case errors.As(err, &clientError):
				status, message = clientError.status, clientError.message
			case errors.Is(err, errConflict):
				status, message = 409, errConflict.Error()
			case errors.As(err, &sizeError):
				status, message = 413, "Images must be smaller than 30 MB."
			default:
				log.Printf("Request failed (%s %s): %T", r.Method, r.URL.Path, err)
			}
			respond(w, status, map[string]string{"error": message})
		}
	}
}

func (a *app) authorized(r *http.Request) bool {
	if a.config.AuthMode == "proxy" {
		// This mode requires the deployment's NetworkPolicy and trusted ForwardAuth gate.
		return strings.EqualFold(r.Header.Get("X-Auth-Request-Email"), a.config.AllowedEmail)
	}
	cookie, err := r.Cookie("photo_session")
	if err != nil {
		return false
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	return a.sessions[cookie.Value].After(time.Now())
}

func (a *app) login(w http.ResponseWriter, r *http.Request) error {
	if a.config.AuthMode != "password" {
		return &requestError{403, "Sign in through Pocket ID."}
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	now := time.Now()
	active := a.loginAttempts[:0]
	for _, attempt := range a.loginAttempts {
		if now.Sub(attempt) < time.Minute {
			active = append(active, attempt)
		}
	}
	a.loginAttempts = active
	if len(active) >= 10 {
		return &requestError{429, "Too many sign-in attempts. Try again in a minute."}
	}
	a.loginAttempts = append(a.loginAttempts, now)
	actual, expected := sha256.Sum256([]byte(input.Password)), sha256.Sum256([]byte(a.config.Password))
	if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return &requestError{401, "Incorrect password."}
	}
	for token, expires := range a.sessions {
		if !expires.After(now) {
			delete(a.sessions, token)
		}
	}
	if len(a.sessions) >= 100 {
		for token := range a.sessions {
			delete(a.sessions, token)
			break
		}
	}
	token := identifier() + identifier()
	a.sessions[token] = now.Add(12 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "photo_session", Value: token, Path: "/", HttpOnly: true, Secure: !a.config.Development, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]bool{"ok": true})
	return nil
}

func (a *app) getCatalog(w http.ResponseWriter, r *http.Request) error {
	c, etag, err := a.load(r.Context(), "private", "catalog.json")
	if err != nil {
		return err
	}
	return a.respondCatalog(w, r, c, etag)
}

// Synchronous clients receive the live comparison; background changes let the
// browser refresh it separately instead of repeating remote reads in the worker.
func (a *app) respondCatalog(w http.ResponseWriter, r *http.Request, c catalog, etag string) error {
	if r.Context().Value(operationContext{}) == true {
		respond(w, 200, map[string]bool{"ok": true})
		return nil
	}
	live, liveETag, err := a.load(r.Context(), "public", "index.json")
	if err != nil {
		return err
	}
	changes := c.publicRelease(a.config.PublicURL).changesFrom(live)
	liveProtected, _, err := a.loadProtected(r.Context(), live)
	if err != nil {
		return err
	}
	protectedChanges := c.protectedRelease().changesFrom(liveProtected)
	changes.ProtectedPhotos, changes.ProtectedAlbums = protectedChanges.Photos, protectedChanges.Albums
	changes.Changed = changes.Changed || protectedChanges.Changed
	var publishedRevision *int
	if liveETag != "" {
		publishedRevision = &live.Revision
		// Older releases can differ despite matching the private revision.
		if live.Revision == c.Revision && changes.Changed {
			publishedRevision = nil
		}
	}
	if etag == "" {
		etag = "new"
	}
	// Republication backfills live hashes without rewriting drafts. Reuse the
	// already-loaded manifests for Studio previews, keeping its saved ETag intact.
	hashes := map[string]string{}
	for _, release := range []catalog{live, liveProtected} {
		for _, p := range release.Photos {
			if p.BlurHash != "" {
				hashes[p.ID] = p.BlurHash
			}
		}
	}
	c.Photos = append([]photo{}, c.Photos...)
	for i := range c.Photos {
		if c.Photos[i].BlurHash == "" {
			c.Photos[i].BlurHash = hashes[c.Photos[i].ID]
		}
	}
	c.Albums = append([]album{}, c.Albums...)
	for i := range c.Albums {
		item := &c.Albums[i]
		item.PasswordSet = item.PasswordHash != ""
		item.PasswordHash, item.AccessVersion = "", ""
		item.LinkHash = ""
	}
	respond(w, 200, map[string]any{"catalog": c, "etag": etag, "publishedRevision": publishedRevision, "publicationChanges": changes, "publication": a.publicationStatus(), "operation": a.operationStatus(), "publicURL": a.config.PublicURL, "authMode": a.config.AuthMode, "development": a.config.Development, "privateAlbumsEnabled": a.config.PrivateAlbums, "galleryURL": a.config.GalleryURL})
	return nil
}

func checkETag(r *http.Request, etag string) error {
	if etag == "" {
		etag = "new"
	}
	if r.Header.Get("If-Match") != etag {
		return errConflict
	}
	return nil
}

func (a *app) mutate(w http.ResponseWriter, r *http.Request, change func(*catalog) error) error {
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
	if err := change(&c); err != nil {
		return err
	}
	next, err := a.save(r.Context(), &c, etag)
	if err != nil {
		return err
	}
	return a.respondCatalog(w, r, c, next)
}

func (a *app) upload(w http.ResponseWriter, r *http.Request) error {
	return a.mutate(w, r, func(c *catalog) error {
		r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		filename, err := url.PathUnescape(r.Header.Get("X-Filename"))
		if err != nil || len(filename) > 255 {
			return badRequest("Invalid filename.")
		}
		if filename == "" {
			filename = "Untitled"
		}
		width, height, exif, capturedDate, variants, err := processImage(body)
		if err != nil {
			return err
		}
		id := identifier()
		if err := a.store.Write(r.Context(), "private", "originals/"+id, body, "application/octet-stream", "no-store", nil); err != nil {
			return err
		}
		hash := ""
		for _, variant := range variants {
			if variant.Size == 640 {
				hash = previewBlurHash(variant.Body)
			}
			if err := a.store.Write(r.Context(), "private", fmt.Sprintf("photos/%s/%d.jpg", id, variant.Size), variant.Body, "image/jpeg", "no-store", nil); err != nil {
				return err
			}
		}
		c.Photos = append(c.Photos, photo{ID: id, Filename: filename, Tags: []string{}, UploadedAt: now(), CapturedDate: capturedDate, Width: width, Height: height, Exif: exif, BlurHash: hash})
		return nil
	})
}

func (a *app) previewImage(w http.ResponseWriter, r *http.Request) error {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(r.PathValue("id")) {
		return &requestError{404, "Photo not found."}
	}
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "1280"
	}
	if size != "640" && size != "1280" {
		return badRequest("Preview size must be 640 or 1280.")
	}
	c, _, err := a.load(r.Context(), "private", "catalog.json")
	if err != nil {
		return err
	}
	found := false
	for _, p := range c.Photos {
		found = found || (p.ID == r.PathValue("id") && !p.Deleting)
	}
	if !found {
		return &requestError{404, "Photo not found."}
	}
	key := "photos/" + r.PathValue("id") + "/" + size + ".jpg"
	location, err := a.store.PreviewURL(r.Context(), key, r.Method)
	if err != nil {
		return err
	}
	if location != "" {
		http.Redirect(w, r, location, http.StatusTemporaryRedirect)
		return nil
	}
	value, err := a.store.Read(r.Context(), "private", key)
	if err != nil {
		return err
	}
	if value == nil {
		return &requestError{404, "Photo not found."}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(value.Body)
	return nil
}

func (a *app) previewVideo(w http.ResponseWriter, r *http.Request) error {
	c, _, err := a.load(r.Context(), "private", "catalog.json")
	if err != nil {
		return err
	}
	id, found := r.PathValue("id"), false
	for _, p := range c.Photos {
		found = found || (p.ID == id && p.Kind == "video" && !p.Deleting)
	}
	if !found {
		return &requestError{404, "Video not found."}
	}
	key := "photos/" + id + "/video.mp4"
	location, err := a.store.PreviewURL(r.Context(), key, r.Method)
	if err != nil {
		return err
	}
	if location != "" {
		http.Redirect(w, r, location, http.StatusTemporaryRedirect)
		return nil
	}
	size, err := a.store.Size(r.Context(), "private", key)
	if err != nil {
		return err
	}
	offset, length, status := int64(0), size, 200
	if value := r.Header.Get("Range"); value != "" {
		invalid := func() error {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			return &requestError{416, "Requested range is unavailable."}
		}
		match := regexp.MustCompile(`^bytes=(\d*)-(\d*)$`).FindStringSubmatch(value)
		if match == nil || (match[1] == "" && match[2] == "") {
			return invalid()
		}
		if match[1] == "" {
			suffix, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil || suffix < 1 {
				return invalid()
			}
			length = min(suffix, size)
			offset = size - length
		} else {
			start, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil || start >= size {
				return invalid()
			}
			offset = start
			end := size - 1
			if match[2] != "" {
				end, err = strconv.ParseInt(match[2], 10, 64)
				if err != nil || end < start {
					return invalid()
				}
			}
			length = min(end, size-1) - start + 1
		}
		status = 206
	}
	var body io.ReadCloser
	if r.Method != "HEAD" {
		body, err = a.store.Open(r.Context(), "private", key, offset, length)
		if err != nil {
			return err
		}
		defer body.Close()
	}
	if status == 206 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size))
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == "HEAD" {
		w.WriteHeader(status)
		return nil
	}
	w.WriteHeader(status)
	_, err = io.CopyN(w, body, length)
	if err != nil {
		log.Printf("Video preview stream interrupted: %T", err)
	}
	return nil
}

func (a *app) editPhoto(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Alt          string   `json:"alt"`
		Caption      string   `json:"caption"`
		Location     *string  `json:"location"`
		Tags         []string `json:"tags"`
		CapturedDate string   `json:"capturedDate"`
		Published    bool     `json:"published"`
		AlbumIDs     []string `json:"albumIds"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	return a.mutate(w, r, func(c *catalog) error {
		input.Alt, input.Caption = strings.TrimSpace(input.Alt), strings.TrimSpace(input.Caption)
		if len(input.Alt) > 1000 || len(input.Caption) > 4000 || len(input.Tags) > 30 {
			return badRequest("Alt text, caption or tags are too long.")
		}
		if input.Location != nil {
			*input.Location = strings.TrimSpace(*input.Location)
			if len(*input.Location) > 300 {
				return badRequest("Location must be at most 300 bytes.")
			}
		}
		if input.CapturedDate != "" {
			if _, err := time.Parse("2006-01-02", input.CapturedDate); err != nil {
				return badRequest("Enter a valid capture date.")
			}
		}
		tags := []string{}
		for _, tag := range input.Tags {
			tag = strings.TrimSpace(tag)
			if len(tag) > 80 {
				return badRequest("Tags must be shorter than 80 characters.")
			}
			if tag != "" && !contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		albumIDs := []string{}
		for _, item := range c.Albums {
			albumIDs = append(albumIDs, item.ID)
		}
		for _, id := range input.AlbumIDs {
			if !contains(albumIDs, id) {
				return badRequest("Unknown album.")
			}
		}
		for i, p := range c.Photos {
			if p.ID != r.PathValue("id") {
				continue
			}
			if p.Deleting {
				return &requestError{409, "This photo is being deleted. Reload and retry deletion."}
			}
			if p.Archived {
				return &requestError{409, "Restore this archived photo before editing it."}
			}
			p.Alt, p.Caption, p.Tags, p.Published, p.CapturedDate = input.Alt, input.Caption, tags, input.Published, input.CapturedDate
			if input.Location != nil {
				p.Location = *input.Location
			}
			c.Photos[i] = p
			for i := range c.Albums {
				item := &c.Albums[i]
				selected := contains(input.AlbumIDs, item.ID)
				if selected && !contains(item.PhotoIDs, p.ID) {
					item.PhotoIDs = append(item.PhotoIDs, p.ID)
				}
				if !selected {
					remaining := []string{}
					for _, id := range item.PhotoIDs {
						if id != p.ID {
							remaining = append(remaining, id)
						}
					}
					item.PhotoIDs = remaining
					if item.CoverID == p.ID {
						item.CoverID = ""
					}
				}
			}
			return nil
		}
		return &requestError{404, "Photo not found."}
	})
}

func (a *app) addToPhotostream(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		PhotoIDs []string `json:"photoIds"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	return a.mutate(w, r, func(c *catalog) error {
		if len(input.PhotoIDs) == 0 {
			return badRequest("Select at least one photo or video.")
		}
		selected := make(map[string]bool, len(input.PhotoIDs))
		for _, id := range input.PhotoIDs {
			if selected[id] {
				return badRequest("Select each photo or video only once.")
			}
			selected[id] = true
		}
		found := 0
		for i, p := range c.Photos {
			if !selected[p.ID] {
				continue
			}
			if p.Archived || p.Archiving || p.Deleting {
				return &requestError{409, "A selected item is archived or being removed. Reload and choose again."}
			}
			c.Photos[i].Published = true
			found++
		}
		if found != len(selected) {
			return &requestError{404, "A selected photo or video no longer exists. Reload and choose again."}
		}
		return nil
	})
}

func (a *app) archivePhoto(w http.ResponseWriter, r *http.Request) error {
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	c, etag, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	if err := checkETag(r, etag); err != nil {
		return err
	}
	index := -1
	for i := range c.Photos {
		if c.Photos[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return &requestError{404, "Photo not found."}
	}
	p := &c.Photos[index]
	if p.Deleting {
		return &requestError{409, "Finish deleting this photo first."}
	}
	if r.PathValue("action") == "restore" {
		if !p.Archived {
			return badRequest("This photo is not archived.")
		}
		if p.Archiving {
			return &requestError{409, "Retry archive to finish manifest cleanup before restoring."}
		}
		p.Archived, p.Published = false, false
	} else {
		p.Archived, p.Archiving, p.Published = true, true, false
		etag, err = a.save(ctx, &c, etag)
		if err != nil {
			return err
		}
		// Redact existing releases only, never publish unrelated working edits.
		if err := a.redactPhoto(ctx, "public", id, true); err != nil {
			return err
		}
		if a.config.PrivateAlbums {
			if err := a.redactPhoto(ctx, "protected", id, true); err != nil {
				return err
			}
		}
		p.Archiving = false
	}
	next, err := a.save(ctx, &c, etag)
	if err != nil {
		return err
	}
	return a.respondCatalog(w, r, c, next)
}

func (a *app) deletePhoto(w http.ResponseWriter, r *http.Request) error {
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	c, etag, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	if err := checkETag(r, etag); err != nil {
		return err
	}
	found := false
	var media photo
	for i := range c.Photos {
		if c.Photos[i].ID == id {
			found = true
			media = c.Photos[i]
			// Persist intent before any destructive writes. A retry after a failed
			// delete or restart resumes cleanup without allowing republication.
			if !c.Photos[i].Deleting {
				c.Photos[i].Deleting = true
				etag, err = a.save(ctx, &c, etag)
				if err != nil {
					return err
				}
			}
			break
		}
	}
	if !found {
		return &requestError{404, "Photo not found."}
	}
	// Change only the live photo and its memberships, never publish other drafts.
	if err := a.redactPhoto(ctx, "public", id, false); err != nil {
		return err
	}
	buckets := []string{"public", "private"}
	if a.config.PrivateAlbums {
		if err := a.redactPhoto(ctx, "protected", id, true); err != nil {
			return err
		}
		buckets = append(buckets, "protected")
	}
	for _, bucket := range buckets {
		for _, key := range media.files() {
			if err := a.store.Delete(ctx, bucket, key); err != nil {
				return err
			}
		}
	}
	if err := a.store.Delete(ctx, "private", "originals/"+id); err != nil {
		return err
	}
	c.removePhoto(id)
	next, err := a.save(ctx, &c, etag)
	if err != nil {
		return err
	}
	return a.respondCatalog(w, r, c, next)
}

func (a *app) createAlbum(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Title string `json:"title"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	return a.mutate(w, r, func(c *catalog) error {
		title := strings.TrimSpace(input.Title)
		if title == "" || len(title) > 150 {
			return badRequest("Give the album a title, up to 150 characters.")
		}
		id := identifier()
		c.Albums = append(c.Albums, album{ID: id, Title: title, Slug: "album-" + id[:8], PhotoIDs: []string{}})
		return nil
	})
}

func (a *app) editAlbum(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Title          string   `json:"title"`
		Slug           string   `json:"slug"`
		Description    string   `json:"description"`
		Published      bool     `json:"published"`
		PhotoIDs       []string `json:"photoIds"`
		CoverID        string   `json:"coverId"`
		Access         string   `json:"access"`
		Password       string   `json:"password"`
		RegenerateLink bool     `json:"regenerateLink"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	return a.mutate(w, r, func(c *catalog) error {
		input.Title, input.Description = strings.TrimSpace(input.Title), strings.TrimSpace(input.Description)
		if input.Title == "" || len(input.Title) > 150 || len(input.Description) > 4000 || len(input.Slug) > 100 || !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(input.Slug) {
			return badRequest("Use a title and a lowercase, hyphen-separated URL slug.")
		}
		for _, other := range c.Albums {
			if other.ID != r.PathValue("id") && other.Slug == input.Slug {
				return badRequest("Another album uses that URL slug.")
			}
		}
		available := []string{}
		for _, p := range c.Photos {
			available = append(available, p.ID)
		}
		selected := []string{}
		for _, id := range input.PhotoIDs {
			if !contains(available, id) || contains(selected, id) {
				return badRequest("Album photos must exist and must not be duplicated.")
			}
			selected = append(selected, id)
		}
		if input.CoverID != "" && !contains(selected, input.CoverID) {
			return badRequest("The cover must be a photo in the album.")
		}
		for i, item := range c.Albums {
			if item.ID == r.PathValue("id") {
				access := input.Access
				if access == "" {
					access = item.Access
				}
				if access != "" && access != "public" && access != "password" && access != "link" {
					return badRequest("Choose public, private share link, or password-protected access.")
				}
				if input.RegenerateLink && access != "link" {
					return badRequest("Choose private share link access before regenerating a link.")
				}
				next := album{ID: item.ID, Title: input.Title, Slug: input.Slug, Description: input.Description, Published: input.Published, PhotoIDs: selected, CoverID: input.CoverID}
				if access == "password" {
					if !a.config.PrivateAlbums {
						return badRequest("Protected album storage is not configured.")
					}
					next.Access, next.PasswordHash, next.AccessVersion = access, item.PasswordHash, item.AccessVersion
					if input.Password != "" {
						hash, err := albumPassword(input.Password)
						if err != nil {
							return err
						}
						next.PasswordHash, next.AccessVersion = hash, identifier()
					}
					if next.PasswordHash == "" {
						return badRequest("Set a password before protecting this album.")
					}
					if item.Published != next.Published {
						next.AccessVersion = identifier()
					}
				} else if input.Password != "" {
					return badRequest("Choose password-protected access before setting a password.")
				} else if access == "link" {
					if !a.config.PrivateAlbums {
						return badRequest("Protected album storage is not configured.")
					}
					next.Access, next.LinkToken, next.AccessVersion = "link", item.LinkToken, item.AccessVersion
					if item.Access != "link" || next.LinkToken == "" || input.RegenerateLink {
						next.LinkToken = identifier() + identifier()
						next.AccessVersion = identifier()
					}
					if item.Published != next.Published {
						next.AccessVersion = identifier()
					}
					next.LinkHash = fmt.Sprintf("%x", sha256.Sum256([]byte(next.LinkToken)))
				}
				c.Albums[i] = next
				return nil
			}
		}
		return &requestError{404, "Album not found."}
	})
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("POST /api/login", a.handler(a.login))
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("photo_session"); err == nil {
			a.authMu.Lock()
			delete(a.sessions, cookie.Value)
			a.authMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "photo_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !a.config.Development, SameSite: http.SameSiteStrictMode})
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/catalog", a.handler(a.getCatalog))
	mux.HandleFunc("POST /api/uploads", a.handler(a.createUpload))
	mux.HandleFunc("POST /api/uploads/process", a.change(a.processUploads))
	mux.HandleFunc("POST /api/uploads/{id}/complete", a.change(a.completeUpload))
	mux.HandleFunc("DELETE /api/uploads/{id}", a.handler(a.cancelUpload))
	if a.config.LocalRoot != "" {
		mux.HandleFunc("PUT /api/uploads/{id}/parts/{part}", a.handler(a.localUploadPart))
	}
	mux.HandleFunc("POST /api/photos", a.handler(a.upload))
	mux.HandleFunc("GET /api/photos/{id}/image", a.handler(a.previewImage))
	mux.HandleFunc("GET /api/photos/{id}/video", a.handler(a.previewVideo))
	mux.HandleFunc("PATCH /api/photos/{id}", a.change(a.editPhoto))
	mux.HandleFunc("POST /api/photostream", a.change(a.addToPhotostream))
	mux.HandleFunc("DELETE /api/photos/{id}", a.change(a.deletePhoto))
	mux.HandleFunc("POST /api/photos/{id}/{action}", a.change(func(w http.ResponseWriter, r *http.Request) error {
		if action := r.PathValue("action"); action != "archive" && action != "restore" {
			return &requestError{404, "Unknown photo action."}
		}
		return a.archivePhoto(w, r)
	}))
	mux.HandleFunc("POST /api/albums", a.handler(a.createAlbum))
	mux.HandleFunc("PUT /api/albums/{id}", a.change(a.editAlbum))
	mux.HandleFunc("DELETE /api/albums/{id}", a.change(func(w http.ResponseWriter, r *http.Request) error {
		return a.mutate(w, r, func(c *catalog) error {
			for i, item := range c.Albums {
				if item.ID == r.PathValue("id") {
					c.Albums = append(c.Albums[:i], c.Albums[i+1:]...)
					return nil
				}
			}
			return &requestError{404, "Album not found."}
		})
	}))
	mux.HandleFunc("POST /api/publish", a.handler(a.startPublication))
	mux.HandleFunc("GET /api/publication", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"publication": a.publicationStatus()})
	})
	mux.HandleFunc("GET /api/operation", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"operation": a.operationStatus()})
	})
	if a.config.LocalRoot != "" {
		mux.HandleFunc("GET /media/{key...}", func(w http.ResponseWriter, r *http.Request) {
			key := r.PathValue("key")
			if !fs.ValidPath(key) || !(strings.HasSuffix(key, ".json") || strings.HasSuffix(key, ".jpg") || strings.HasSuffix(key, ".mp4")) {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(a.config.LocalRoot, "public", filepath.FromSlash(key)))
		})
	}
	public, _ := fs.Sub(assets, "public")
	mux.Handle("GET /", http.FileServer(http.FS(public)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: blob: data:; media-src 'self' "+a.config.UploadOrigin+"; connect-src 'self' "+a.config.UploadOrigin+"; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" && r.Method != "HEAD" {
				origin := a.config.Origin
				if origin == "" && a.config.Development {
					parsed, err := url.Parse(r.Header.Get("Origin"))
					if err == nil && parsed.Host == r.Host && (parsed.Scheme == "http" || parsed.Scheme == "https") {
						origin = parsed.Scheme + "://" + parsed.Host
					}
				}
				if origin == "" || r.Header.Get("Origin") != origin {
					respond(w, 403, map[string]string{"error": "Request origin is not allowed."})
					return
				}
			}
			if r.URL.Path != "/api/login" && !a.authorized(r) {
				respond(w, 401, map[string]string{"error": "Please sign in."})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func main() {
	development := os.Getenv("APP_ENV") == "development"
	config := configuration{Development: development, Origin: os.Getenv("APP_ORIGIN"), PublicURL: strings.TrimRight(os.Getenv("PUBLIC_MEDIA_URL"), "/"), AuthMode: os.Getenv("AUTH_MODE"), Password: os.Getenv("ADMIN_PASSWORD"), AllowedEmail: os.Getenv("ALLOWED_EMAIL")}
	if config.AuthMode == "" {
		config.AuthMode = "password"
	}
	if filename := os.Getenv("ADMIN_PASSWORD_FILE"); filename != "" {
		body, err := os.ReadFile(filename)
		if err != nil {
			log.Fatal("Cannot read ADMIN_PASSWORD_FILE")
		}
		config.Password = strings.TrimSpace(string(body))
	}
	if config.AuthMode != "proxy" && config.AuthMode != "password" {
		log.Fatal("AUTH_MODE must be proxy or password")
	}
	if config.AuthMode == "proxy" && config.AllowedEmail == "" {
		log.Fatal("Proxy mode requires ALLOWED_EMAIL and a trusted ForwardAuth/NetworkPolicy deployment")
	}
	if config.AuthMode == "password" && len(config.Password) < 16 {
		log.Fatal("ADMIN_PASSWORD must have at least 16 characters")
	}
	validHTTPS := func(value string) bool {
		u, err := url.Parse(value)
		return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
	}
	if !development && (!validHTTPS(config.Origin) || strings.TrimRight(config.Origin, "/") != config.Origin) {
		log.Fatal("Production requires an HTTPS APP_ORIGIN without a trailing slash")
	}
	var store storage
	if os.Getenv("PHOTO_STORAGE") == "local" {
		if !development {
			log.Fatal("Local storage is only allowed with APP_ENV=development")
		}
		config.LocalRoot = os.Getenv("LOCAL_DATA_DIR")
		if config.LocalRoot == "" {
			config.LocalRoot = ".local-data"
		}
		config.PublicURL = "/media"
		config.PrivateAlbums = true
		store = &localStorage{root: config.LocalRoot}
	} else {
		for _, name := range []string{"R2_ENDPOINT", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PRIVATE_BUCKET", "R2_PUBLIC_BUCKET", "PUBLIC_MEDIA_URL"} {
			if os.Getenv(name) == "" {
				log.Fatalf("Missing %s", name)
			}
		}
		if !validHTTPS(os.Getenv("R2_ENDPOINT")) || !validHTTPS(config.PublicURL) || os.Getenv("R2_PRIVATE_BUCKET") == os.Getenv("R2_PUBLIC_BUCKET") {
			log.Fatal("Use HTTPS R2/media URLs and separate private/public buckets")
		}
		r2 := newR2Storage(os.Getenv("R2_ENDPOINT"), os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_PRIVATE_BUCKET"), os.Getenv("R2_PUBLIC_BUCKET"))
		endpoint, _ := url.Parse(os.Getenv("R2_ENDPOINT"))
		config.UploadOrigin = endpoint.Scheme + "://" + endpoint.Host
		if protected := os.Getenv("R2_PROTECTED_BUCKET"); protected != "" {
			if protected == os.Getenv("R2_PRIVATE_BUCKET") || protected == os.Getenv("R2_PUBLIC_BUCKET") {
				log.Fatal("Protected delivery requires a separate, non-public bucket")
			}
			r2.buckets["protected"] = protected
			config.PrivateAlbums = true
		}
		store = r2
	}
	config.GalleryURL = strings.TrimRight(os.Getenv("PUBLIC_GALLERY_URL"), "/") + "/"
	if config.GalleryURL == "/" {
		config.GalleryURL = "https://hugo.md/photos/"
	}
	if !development && !validHTTPS(config.GalleryURL) {
		log.Fatal("PUBLIC_GALLERY_URL must be HTTPS")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a := &app{store: store, config: config, sessions: map[string]time.Time{}, background: ctx}
	if err := a.resumeOperation(); err != nil {
		log.Fatalf("Cannot load change state: %T", err)
	}
	if err := a.resumePublication(); err != nil {
		log.Fatalf("Cannot load publication state: %T", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("Photo manager listening on :%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	a.workers.Wait()
}
