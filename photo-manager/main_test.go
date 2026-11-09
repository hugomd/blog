package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

type browser struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	state  struct {
		Catalog catalog `json:"catalog"`
		ETag    string  `json:"etag"`
	}
}

func newBrowser(t *testing.T, store storage) *browser {
	t.Helper()
	a := &app{store: store, config: configuration{Development: true, Password: "test-password-not-real", AuthMode: "password", PublicURL: "/media", LocalRoot: storeRoot(store)}, sessions: map[string]time.Time{}}
	server := httptest.NewServer(a.routes())
	a.config.Origin = server.URL
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, server: server, client: &http.Client{Jar: jar}}
	b.request("POST", "/api/login", map[string]string{"password": "test-password-not-real"}, 200)
	b.request("GET", "/api/catalog", nil, 200)
	return b
}

func storeRoot(store storage) string {
	if local, ok := store.(*localStorage); ok {
		return local.root
	}
	if failing, ok := store.(*failingStorage); ok {
		return storeRoot(failing.storage)
	}
	return ""
}

func (b *browser) request(method, path string, value any, status int) []byte {
	b.t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			b.t.Fatal(err)
		}
	}
	return b.raw(method, path, body, status)
}

func (b *browser) raw(method, path string, body []byte, status int) []byte {
	b.t.Helper()
	req, err := http.NewRequest(method, b.server.URL+path, bytes.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Origin", b.server.URL)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		req.Header.Set("If-Match", b.state.ETag)
	}
	response, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	if response.StatusCode != status {
		b.t.Fatalf("%s %s: got %d, want %d: %s", method, path, response.StatusCode, status, result)
	}
	if status == 200 && (path == "/api/catalog" || (method != "GET" && path != "/api/login" && path != "/api/logout" && path != "/api/publish")) {
		if err := json.Unmarshal(result, &b.state); err != nil {
			b.t.Fatal(err)
		}
	}
	return result
}

func edit(p photo, albums ...string) map[string]any {
	return map[string]any{"alt": p.Alt, "caption": p.Caption, "tags": p.Tags, "capturedDate": p.CapturedDate, "published": p.Published, "albumIds": albums}
}

// Synthetic EXIF includes real GPS IFDs and a private owner field, not just marker text.
// The asymmetric 4000×2400 quadrants also make incorrect orientation/resizing observable.
func locatedJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4000, 2400))
	for y := 0; y < 2400; y++ {
		for x := 0; x < 4000; x++ {
			c := color.RGBA{230, 20, 20, 255}
			if x >= 2000 {
				c = color.RGBA{20, 220, 20, 255}
			}
			if y >= 1200 {
				c = color.RGBA{20, 20, 230, 255}
				if x >= 2000 {
					c = color.RGBA{220, 220, 20, 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var original bytes.Buffer
	if err := jpeg.Encode(&original, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	const exifStart = 74
	const gpsStart = 164
	const dataStart = 218
	var ifd, camera, gps, data bytes.Buffer
	put := func(b *bytes.Buffer, value any) {
		if err := binary.Write(b, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(b *bytes.Buffer, tag, kind uint16, count, value uint32) {
		put(b, tag)
		put(b, kind)
		put(b, count)
		put(b, value)
	}
	appendData := func(body []byte) uint32 { offset := uint32(dataStart + data.Len()); data.Write(body); return offset }
	ascii := func(b *bytes.Buffer, tag uint16, value string) {
		raw := append([]byte(value), 0)
		entry(b, tag, 2, uint32(len(raw)), appendData(raw))
	}
	rational := func(b *bytes.Buffer, tag uint16, n, d uint32) {
		var raw bytes.Buffer
		put(&raw, n)
		put(&raw, d)
		entry(b, tag, 5, 1, appendData(raw.Bytes()))
	}
	put(&ifd, uint16(5))
	ascii(&ifd, 0x010f, "FUJIFILM")
	ascii(&ifd, 0x0110, "X-E4")
	entry(&ifd, 0x0112, 3, 1, 6)
	entry(&ifd, 0x8769, 4, 1, exifStart)
	entry(&ifd, 0x8825, 4, 1, gpsStart)
	put(&ifd, uint32(0))
	put(&camera, uint16(7))
	rational(&camera, 0x829a, 1, 250)
	rational(&camera, 0x829d, 28, 10)
	entry(&camera, 0x8827, 3, 1, 640)
	ascii(&camera, 0x9003, "2022:09:11 08:32:34")
	rational(&camera, 0x920a, 35, 1)
	ascii(&camera, 0xa430, "PRIVATE OWNER")
	ascii(&camera, 0xa434, "XF35mmF1.4 R")
	put(&camera, uint32(0))
	put(&gps, uint16(4))
	entry(&gps, 1, 2, 2, uint32('S'))
	var lat bytes.Buffer
	for _, v := range []uint32{33, 1, 51, 1, 0, 1} {
		put(&lat, v)
	}
	entry(&gps, 2, 5, 3, appendData(lat.Bytes()))
	entry(&gps, 3, 2, 2, uint32('E'))
	var lon bytes.Buffer
	for _, v := range []uint32{151, 1, 12, 1, 0, 1} {
		put(&lon, v)
	}
	entry(&gps, 4, 5, 3, appendData(lon.Bytes()))
	put(&gps, uint32(0))
	var payload bytes.Buffer
	payload.WriteString("Exif\x00\x00II")
	put(&payload, uint16(42))
	put(&payload, uint32(8))
	payload.Write(ifd.Bytes())
	payload.Write(camera.Bytes())
	payload.Write(gps.Bytes())
	payload.Write(data.Bytes())
	var output bytes.Buffer
	output.Write(original.Bytes()[:2])
	output.Write([]byte{0xff, 0xe1})
	binary.Write(&output, binary.BigEndian, uint16(payload.Len()+2))
	output.Write(payload.Bytes())
	output.Write(original.Bytes()[2:])
	fields, err := exif.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	latValue, lonValue, err := fields.LatLong()
	if err != nil || latValue > -33 || lonValue < 151 {
		t.Fatalf("Fixture must have GPS: %v %v %v", latValue, lonValue, err)
	}
	return output.Bytes()
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 53, 29))
	img.SetNRGBA(30, 10, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUploadStripsLocationAndPreservesCameraInformation(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	original := locatedJPEG(t)
	b.raw("POST", "/api/photos", original, 200)
	p := b.state.Catalog.Photos[0]
	if p.Width != 2400 || p.Height != 4000 || p.Published {
		t.Fatalf("Wrong oriented dimensions or default publication: %+v", p)
	}
	want := map[string]string{"Make": "FUJIFILM", "Camera": "X-E4", "Lens": "XF35mmF1.4 R", "ISO": "640", "Shutter": "1/250 s", "Aperture": "ƒ/2.8", "Focal length": "35 mm"}
	if !reflect.DeepEqual(p.Exif, want) || p.CapturedDate != "2022-09-11" {
		t.Fatalf("Incorrect camera metadata: %+v", p)
	}
	for size, wantDimensions := range map[int]image.Point{640: {384, 640}, 1280: {768, 1280}, 2560: {1536, 2560}} {
		value, err := store.Read(t.Context(), "private", fmt.Sprintf("photos/%s/%d.jpg", p.ID, size))
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(value.Body))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Size() != wantDimensions {
			t.Fatalf("%d derivative: got %v, want %v", size, img.Bounds().Size(), wantDimensions)
		}
		r, g, blue, _ := img.At(20, 20).RGBA()
		if blue < 45000 || r > 12000 || g > 12000 {
			t.Fatal("Photo did not rotate 90° clockwise")
		}
		if bytes.Contains(value.Body, []byte("Exif\x00\x00")) || bytes.Contains(value.Body, []byte("PRIVATE OWNER")) {
			t.Fatal("Embedded metadata survived re-encoding")
		}
	}
	public, err := store.Read(t.Context(), "public", fmt.Sprintf("photos/%s/640.jpg", p.ID))
	if err != nil || public != nil {
		t.Fatal("Draft image leaked into public storage")
	}
	private, err := store.Read(t.Context(), "private", "originals/"+p.ID)
	if err != nil || !bytes.Equal(private.Body, original) {
		t.Fatal("Private original was not preserved")
	}
	p.Alt = "A city scene"
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/publish", nil, 200)
	manifest := b.request("GET", "/media/index.json", nil, 200)
	if bytes.Contains(manifest, []byte("PRIVATE OWNER")) || bytes.Contains(manifest, []byte("GPS")) || bytes.Contains(manifest, []byte("filename")) {
		t.Fatal("Private metadata leaked to public manifest")
	}
	var live catalog
	if err := json.Unmarshal(manifest, &live); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.Photos[0].Exif, want) {
		t.Fatal("Camera metadata missing from public manifest")
	}
	copy := b.request("GET", live.Photos[0].Thumbnail, nil, 200)
	if bytes.Contains(copy, []byte("Exif\x00\x00")) {
		t.Fatal("Public derivative contains EXIF location data")
	}
}

func TestAlbumsDoNotOwnThePhotostream(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	first, standalone, draft := b.state.Catalog.Photos[0], b.state.Catalog.Photos[1], b.state.Catalog.Photos[2]
	first.Alt = "First"
	first.Published = true
	standalone.Alt = "Standalone"
	standalone.Published = true
	b.request("PATCH", "/api/photos/"+first.ID, edit(first), 200)
	b.request("PATCH", "/api/photos/"+standalone.ID, edit(standalone), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "New York"}, 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Favorites"}, 200)
	for i, album := range b.state.Catalog.Albums {
		album.Published = true
		album.Slug = fmt.Sprintf("album-%d", i)
		album.PhotoIDs = []string{draft.ID, first.ID}
		album.CoverID = draft.ID
		b.request("PUT", "/api/albums/"+album.ID, map[string]any{"title": album.Title, "slug": album.Slug, "description": "", "published": true, "photoIds": album.PhotoIDs, "coverId": album.CoverID}, 200)
	}
	b.request("POST", "/api/publish", nil, 200)
	var live catalog
	if err := json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Photos) != 2 || live.Photos[0].ID != standalone.ID || live.Photos[1].ID != first.ID {
		t.Fatalf("Stream must contain standalone + album photo once, newest first: %+v", live.Photos)
	}
	for _, album := range live.Albums {
		if !reflect.DeepEqual(album.PhotoIDs, []string{first.ID}) || album.CoverID != first.ID {
			t.Fatalf("Draft photo or cover leaked: %+v", album)
		}
	}
	b.request("DELETE", "/api/albums/"+b.state.Catalog.Albums[0].ID, nil, 200)
	if len(b.state.Catalog.Photos) != 3 {
		t.Fatal("Removing an album deleted its photos")
	}
	stale := b.state.ETag
	b.request("POST", "/api/albums", map[string]string{"title": "Another"}, 200)
	b.state.ETag = stale
	b.request("POST", "/api/albums", map[string]string{"title": "Must not overwrite"}, 409)
	b.request("GET", "/api/catalog", nil, 200)
	for _, album := range b.state.Catalog.Albums {
		if album.Title == "Must not overwrite" {
			t.Fatal("Stale tab overwrote catalog")
		}
	}
}

type failingStorage struct {
	storage
	fail bool
}

func (s *failingStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if s.fail && bucket == "public" && strings.HasSuffix(key, "/1280.jpg") {
		return errors.New("simulated storage outage")
	}
	return s.storage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func TestFailedPublicationDoesNotReplaceLiveIndex(t *testing.T) {
	store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt = "Initial photo"
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/publish", nil, 200)
	before := b.request("GET", "/media/index.json", nil, 200)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p = b.state.Catalog.Photos[1]
	p.Alt = "New photo"
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	store.fail = true
	b.request("POST", "/api/publish", nil, 500)
	if !bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Failed image upload replaced live index")
	}
	store.fail = false
	b.request("POST", "/api/publish", nil, 200)
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 2 {
		t.Fatal("Retry did not publish both photos")
	}
}

func TestAuthenticationAndInputBoundaries(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	response, err := http.Get(b.server.URL + "/api/catalog")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("Anonymous catalog access allowed")
	}
	request, _ := http.NewRequest("POST", b.server.URL+"/api/albums", strings.NewReader(`{"title":"CSRF"}`))
	request.Header.Set("Origin", "https://attacker.invalid")
	response, err = b.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("Cross-origin mutation allowed")
	}
	b.raw("POST", "/api/photos", []byte("not an image"), 400)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 400)
	p.Published = false
	p.CapturedDate = "2026-02-31"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 400)
	b.request("POST", "/api/albums", map[string]string{"title": "Album"}, 200)
	album := b.state.Catalog.Albums[0]
	b.request("PUT", "/api/albums/"+album.ID, map[string]any{"title": "Album", "slug": "album", "description": "", "published": true, "photoIds": []string{p.ID, p.ID}, "coverId": p.ID}, 400)
	preview, _ := http.Get(b.server.URL + "/api/photos/" + p.ID + "/image")
	preview.Body.Close()
	if preview.StatusCode != 401 {
		t.Fatal("Anonymous draft preview allowed")
	}
	b.request("POST", "/api/logout", nil, 200)
	b.request("GET", "/api/catalog", nil, 401)
}

func TestProxyAuthenticationRequiresAllowedIdentity(t *testing.T) {
	a := &app{store: &localStorage{root: t.TempDir()}, config: configuration{AuthMode: "proxy", AllowedEmail: "admin@example.invalid", Origin: "https://photos.example.invalid"}}
	for _, email := range []string{"", "other@example.invalid", "admin@example.invalid"} {
		r := httptest.NewRequest("GET", "/api/catalog", nil)
		r.Header.Set("X-Auth-Request-Email", email)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		want := 401
		if email == "admin@example.invalid" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("Identity %q: got %d want %d", email, w.Code, want)
		}
	}
}
