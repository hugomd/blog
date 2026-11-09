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
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed public/*
var assets embed.FS

type configuration struct {
	Origin, PublicURL, AuthMode, Password, AllowedEmail, LocalRoot string
	Development                                                    bool
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

// Reads and mutations return the same live comparison so saved edits refresh it.
func (a *app) respondCatalog(w http.ResponseWriter, r *http.Request, c catalog, etag string) error {
	live, liveETag, err := a.load(r.Context(), "public", "index.json")
	if err != nil {
		return err
	}
	changes := c.publicRelease(a.config.PublicURL).changesFrom(live)
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
	respond(w, 200, map[string]any{"catalog": c, "etag": etag, "publishedRevision": publishedRevision, "publicationChanges": changes, "publication": a.publicationStatus(), "publicURL": a.config.PublicURL, "authMode": a.config.AuthMode, "development": a.config.Development})
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
		for _, variant := range variants {
			if err := a.store.Write(r.Context(), "private", fmt.Sprintf("photos/%s/%d.jpg", id, variant.Size), variant.Body, "image/jpeg", "no-store", nil); err != nil {
				return err
			}
		}
		c.Photos = append(c.Photos, photo{ID: id, Filename: filename, Tags: []string{}, UploadedAt: now(), CapturedDate: capturedDate, Width: width, Height: height, Exif: exif})
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
	value, err := a.store.Read(r.Context(), "private", "photos/"+r.PathValue("id")+"/"+size+".jpg")
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

func (a *app) editPhoto(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Alt          string   `json:"alt"`
		Caption      string   `json:"caption"`
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
		if input.Published && input.Alt == "" {
			return badRequest("Add alt text before publishing this photo.")
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
		keys, err := a.store.List(ctx, "public", "releases/")
		if err != nil {
			return err
		}
		// Redact only existing public data; never publish unrelated working edits.
		for _, key := range append([]string{"index.json"}, keys...) {
			if !strings.HasSuffix(key, ".json") {
				continue
			}
			live, current, err := a.load(ctx, "public", key)
			if err != nil {
				return err
			}
			if current == "" {
				continue
			}
			live.removePhoto(id)
			body, err := encode(live)
			if err != nil {
				return err
			}
			if err := a.store.Write(ctx, "public", key, body, "application/json", "public, max-age=30, must-revalidate", &current); err != nil {
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
	for i := range c.Photos {
		if c.Photos[i].ID == id {
			found = true
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
	live, liveETag, err := a.load(ctx, "public", "index.json")
	if err != nil {
		return err
	}
	if liveETag != "" {
		live.removePhoto(id)
		body, err := encode(live)
		if err != nil {
			return err
		}
		if err := a.store.Write(ctx, "public", "index.json", body, "application/json", "public, max-age=30, must-revalidate", &liveETag); err != nil {
			return err
		}
	}
	for _, bucket := range []string{"public", "private"} {
		for _, size := range []int{640, 1280, 2560} {
			if err := a.store.Delete(ctx, bucket, fmt.Sprintf("photos/%s/%d.jpg", id, size)); err != nil {
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
		Title       string   `json:"title"`
		Slug        string   `json:"slug"`
		Description string   `json:"description"`
		Published   bool     `json:"published"`
		PhotoIDs    []string `json:"photoIds"`
		CoverID     string   `json:"coverId"`
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
				c.Albums[i] = album{ID: item.ID, Title: input.Title, Slug: input.Slug, Description: input.Description, Published: input.Published, PhotoIDs: selected, CoverID: input.CoverID}
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
	mux.HandleFunc("POST /api/photos", a.handler(a.upload))
	mux.HandleFunc("GET /api/photos/{id}/image", a.handler(a.previewImage))
	mux.HandleFunc("PATCH /api/photos/{id}", a.handler(a.editPhoto))
	mux.HandleFunc("DELETE /api/photos/{id}", a.handler(a.deletePhoto))
	mux.HandleFunc("POST /api/photos/{id}/{action}", a.handler(func(w http.ResponseWriter, r *http.Request) error {
		if action := r.PathValue("action"); action != "archive" && action != "restore" {
			return &requestError{404, "Unknown photo action."}
		}
		return a.archivePhoto(w, r)
	}))
	mux.HandleFunc("POST /api/albums", a.handler(a.createAlbum))
	mux.HandleFunc("PUT /api/albums/{id}", a.handler(a.editAlbum))
	mux.HandleFunc("DELETE /api/albums/{id}", a.handler(func(w http.ResponseWriter, r *http.Request) error {
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
	if a.config.LocalRoot != "" {
		mux.HandleFunc("GET /media/{key...}", func(w http.ResponseWriter, r *http.Request) {
			key := r.PathValue("key")
			if !fs.ValidPath(key) || !(strings.HasSuffix(key, ".json") || strings.HasSuffix(key, ".jpg")) {
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: blob:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
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
		store = newR2Storage(os.Getenv("R2_ENDPOINT"), os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_PRIVATE_BUCKET"), os.Getenv("R2_PUBLIC_BUCKET"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a := &app{store: store, config: config, sessions: map[string]time.Time{}, background: ctx}
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
