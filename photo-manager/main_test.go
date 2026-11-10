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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/buckket/go-blurhash"
	"github.com/rwcarlsen/goexif/exif"
)

type browser struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	stop   func()
	state  struct {
		Catalog catalog `json:"catalog"`
		ETag    string  `json:"etag"`
	}
}

func newBrowser(t *testing.T, store storage) *browser {
	t.Helper()
	background, cancel := context.WithCancel(t.Context())
	a := &app{store: store, config: configuration{Development: true, Password: "test-password-not-real", AuthMode: "password", PublicURL: "/media", LocalRoot: storeRoot(store), PrivateAlbums: storeRoot(store) != "", GalleryURL: "https://example.test/photos/"}, sessions: map[string]time.Time{}, background: background}
	if err := a.resumeOperation(); err != nil {
		t.Fatal(err)
	}
	if err := a.resumePublication(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.routes())
	a.config.Origin = server.URL
	jar, _ := cookiejar.New(nil)
	stop := func() { cancel(); server.Close(); a.workers.Wait() }
	t.Cleanup(stop)
	b := &browser{t: t, server: server, client: &http.Client{Jar: jar}, stop: stop}
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
	if paused, ok := store.(*pausedCopyStorage); ok {
		return storeRoot(paused.storage)
	}
	if paused, ok := store.(*pausedOperationStorage); ok {
		return storeRoot(paused.storage)
	}
	if paused, ok := store.(*pausedBatchStorage); ok {
		return paused.root
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
		// Decode each response afresh, like fetch().json(); omitted false fields
		// must not inherit true values from the previous response's slice storage.
		b.state.Catalog = catalog{}
		if err := json.Unmarshal(result, &b.state); err != nil {
			b.t.Fatal(err)
		}
	}
	return result
}

func (b *browser) publish(state string) {
	b.t.Helper()
	b.request("POST", "/api/publish", nil, http.StatusAccepted)
	b.waitPublication(state)
}

func (b *browser) waitPublication(state string) publication {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var result struct {
			Publication publication `json:"publication"`
		}
		if err := json.Unmarshal(b.request("GET", "/api/publication", nil, 200), &result); err != nil {
			b.t.Fatal(err)
		}
		if result.Publication.State != "running" {
			if result.Publication.State != state {
				b.t.Fatalf("Publication state: got %+v, want %s", result.Publication, state)
			}
			return result.Publication
		}
		if time.Now().After(deadline) {
			b.t.Fatal("Publication did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func edit(p photo, albums ...string) map[string]any {
	return map[string]any{"alt": p.Alt, "caption": p.Caption, "location": p.Location, "tags": p.Tags, "capturedDate": p.CapturedDate, "published": p.Published, "albumIds": albums}
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

func decodePhotoBlurHash(t *testing.T, hash string) image.Image {
	t.Helper()
	x, y, err := blurhash.Components(hash)
	if err != nil || x != 4 || y != 3 {
		t.Fatalf("Invalid 4×3 BlurHash %q: %v", hash, err)
	}
	img, err := blurhash.Decode(hash, 32, 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	return img
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
	placeholder := decodePhotoBlurHash(t, p.BlurHash)
	// Original quadrants rotate clockwise to blue/red/yellow/green. Check
	// independent decoded colors, not just equality with our encoder helper.
	for _, sample := range []struct {
		x, y int
		want color.RGBA
	}{{8, 8, color.RGBA{20, 20, 230, 255}}, {24, 8, color.RGBA{230, 20, 20, 255}}, {8, 24, color.RGBA{220, 220, 20, 255}}, {24, 24, color.RGBA{20, 220, 20, 255}}} {
		got := color.RGBAModel.Convert(placeholder.At(sample.x, sample.y)).(color.RGBA)
		channels := [][2]uint8{{got.R, sample.want.R}, {got.G, sample.want.G}, {got.B, sample.want.B}}
		// Low-frequency reconstruction mixes neighboring quadrants; preserve
		// the hue/channel dominance, not the original sharp RGB magnitudes.
		for _, high := range channels {
			for _, low := range channels {
				if high[1] > 200 && low[1] == 20 && int(high[0])-int(low[0]) < 50 {
					t.Fatalf("BlurHash lost oriented color at %d,%d: %+v", sample.x, sample.y, got)
				}
			}
		}
	}
	want := map[string]string{"Make": "FUJIFILM", "Camera": "X-E4", "Lens": "XF35mmF1.4 R", "ISO": "640", "Shutter": "1/250 s", "Aperture": "ƒ/2.8", "Focal length": "35 mm"}
	if !reflect.DeepEqual(p.Exif, want) || p.CapturedDate != "2022-09-11" || p.Location != "" {
		t.Fatalf("Incorrect camera metadata: %+v", p)
	}
	for query, dimensions := range map[string]image.Point{"": {768, 1280}, "?size=640": {384, 640}} {
		body := b.request("GET", "/api/photos/"+p.ID+"/image"+query, nil, 200)
		img, err := jpeg.Decode(bytes.NewReader(body))
		if err != nil || img.Bounds().Size() != dimensions {
			t.Fatalf("Wrong preview dimensions for %s: %v", query, err)
		}
	}
	b.request("GET", "/api/photos/"+p.ID+"/image?size=../catalog.json", nil, 400)
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
	b.publish("succeeded")
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
	if live.Photos[0].BlurHash != p.BlurHash || !bytes.Contains(manifest, []byte(`"blurhash"`)) {
		t.Fatal("Upload BlurHash missing from public JSON contract")
	}
	copy := b.request("GET", live.Photos[0].Thumbnail, nil, 200)
	if bytes.Contains(copy, []byte("Exif\x00\x00")) {
		t.Fatal("Public derivative contains EXIF location data")
	}
}

func TestOptionalPhotoLocationPublishesOnlyAfterConfirmation(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Published, p.Location = true, "  New York, NY  "
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	if b.state.Catalog.Photos[0].Location != "New York, NY" {
		t.Fatal("Manual location was not saved and trimmed")
	}
	legacy := edit(b.state.Catalog.Photos[0])
	delete(legacy, "location")
	b.request("PATCH", "/api/photos/"+p.ID, legacy, 200)
	if b.state.Catalog.Photos[0].Location != "New York, NY" {
		t.Fatal("An older client cleared the saved location")
	}
	p.Location = strings.Repeat("a", 300)
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	p.Location += "b"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 400)
	p.Location = "New York, NY"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.publish("succeeded")
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if live.Photos[0].Location != "New York, NY" {
		t.Fatal("Published photo is missing its explicit location")
	}
	p.Location = "   "
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	if !bytes.Contains(b.request("GET", "/media/index.json", nil, 200), []byte(`"location"`)) {
		t.Fatal("A draft edit changed the live publication")
	}
	b.publish("succeeded")
	if bytes.Contains(b.request("GET", "/media/index.json", nil, 200), []byte(`"location"`)) {
		t.Fatal("Empty location was not omitted after publishing")
	}
}

func TestAlbumPublicationReadinessAndStandalonePhotostream(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for range 4 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	first, standalone, inherited, draft := b.state.Catalog.Photos[0], b.state.Catalog.Photos[1], b.state.Catalog.Photos[2], b.state.Catalog.Photos[3]
	first.Alt = "First"
	standalone.Published = true
	b.request("PATCH", "/api/photos/"+first.ID, edit(first), 200)
	b.request("PATCH", "/api/photos/"+standalone.ID, edit(standalone), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "New York"}, 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Favorites"}, 200)
	for i, album := range b.state.Catalog.Albums {
		album.Published = true
		album.Slug = fmt.Sprintf("album-%d", i)
		album.PhotoIDs = []string{inherited.ID, first.ID}
		album.CoverID = inherited.ID
		b.request("PUT", "/api/albums/"+album.ID, map[string]any{"title": album.Title, "slug": album.Slug, "description": "", "published": true, "photoIds": album.PhotoIDs, "coverId": album.CoverID}, 200)
	}
	// Alt text is optional for both standalone and album-inherited photos.
	b.request("PATCH", "/api/photos/"+inherited.ID, edit(inherited, b.state.Catalog.Albums[0].ID, b.state.Catalog.Albums[1].ID), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Draft album"}, 200)
	draftAlbum := b.state.Catalog.Albums[2]
	b.request("PUT", "/api/albums/"+draftAlbum.ID, map[string]any{"title": draftAlbum.Title, "slug": "draft", "published": false, "photoIds": []string{draft.ID}, "coverId": draft.ID}, 200)
	// An old empty publication at this revision must not disable Publish on upgrade.
	legacy, err := encode(catalog{Version: 1, Revision: b.state.Catalog.Revision, Photos: []photo{}, Albums: []album{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(t.Context(), "public", "index.json", legacy, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	var publication struct {
		Revision *int `json:"publishedRevision"`
	}
	if err := json.Unmarshal(b.request("GET", "/api/catalog", nil, 200), &publication); err != nil || publication.Revision != nil {
		t.Fatalf("Legacy publication must need republishing: %+v, %v", publication, err)
	}
	b.publish("succeeded")
	if err := json.Unmarshal(b.request("GET", "/api/catalog", nil, 200), &publication); err != nil || publication.Revision == nil || *publication.Revision != b.state.Catalog.Revision {
		t.Fatalf("Publication must be current after republishing: %+v, %v", publication, err)
	}
	var live catalog
	if err := json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Photos) != 3 || live.Photos[0].ID != inherited.ID || live.Photos[1].ID != standalone.ID || live.Photos[2].ID != first.ID {
		t.Fatalf("Stream must contain standalone + album photo once, newest first: %+v", live.Photos)
	}
	if len(live.Albums) != 2 {
		t.Fatalf("Draft album was published: %+v", live.Albums)
	}
	if live.Photos[0].Alt != "" || live.Photos[1].Alt != "" || live.Photos[2].Alt != "First" {
		t.Fatal("Publishing must retain optional alt text without inventing descriptions")
	}
	for _, p := range live.Photos {
		if !p.Published {
			t.Fatal("Public photo still marked draft")
		}
	}
	for _, album := range live.Albums {
		if !reflect.DeepEqual(album.PhotoIDs, []string{inherited.ID, first.ID}) || album.CoverID != inherited.ID {
			t.Fatalf("Album photos/order/cover missing: %+v", album)
		}
	}
	// Inheritance is derived, not latched into the independent publication flag.
	b.request("GET", "/api/catalog", nil, 200)
	if b.state.Catalog.Photos[0].Published || b.state.Catalog.Photos[2].Published {
		t.Fatal("Publishing changed inherited photos' independent settings")
	}
	b.request("PATCH", "/api/photos/"+inherited.ID, edit(inherited, b.state.Catalog.Albums[1].ID), 200)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 3 {
		t.Fatal("Removing one membership ignored the other published album")
	}
	b.request("PATCH", "/api/photos/"+inherited.ID, edit(inherited, draftAlbum.ID), 200)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 2 || live.Photos[0].ID != standalone.ID || live.Photos[1].ID != first.ID {
		t.Fatalf("Removing the last published membership did not return photo to draft: %+v", live.Photos)
	}
	b.request("DELETE", "/api/albums/"+b.state.Catalog.Albums[0].ID, nil, 200)
	if len(b.state.Catalog.Photos) != 4 {
		t.Fatal("Removing an album deleted its photos")
	}
	remaining := b.state.Catalog.Albums[0]
	b.request("PUT", "/api/albums/"+remaining.ID, map[string]any{"title": remaining.Title, "slug": remaining.Slug, "published": false, "photoIds": remaining.PhotoIDs, "coverId": remaining.CoverID}, 200)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 1 || live.Photos[0].ID != standalone.ID || len(live.Albums) != 0 {
		t.Fatalf("Unpublishing last album must retain only independently ready photos: %+v", live)
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

func TestPhotostreamSelectionIsAtomicAndPublishesSeparately(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	for range 4 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	first, second, archived := b.state.Catalog.Photos[0].ID, b.state.Catalog.Photos[1].ID, b.state.Catalog.Photos[3].ID
	b.request("POST", "/api/albums", map[string]string{"title": "Private collection"}, 200)
	private := b.state.Catalog.Albums[0]
	b.request("PUT", "/api/albums/"+private.ID, map[string]any{"title": private.Title, "slug": private.Slug, "access": "link", "published": true, "photoIds": []string{first}, "coverId": first}, 200)
	p := b.state.Catalog.Photos[0]
	p.Alt, p.Caption, p.Tags, p.CapturedDate = "Mountain", "Keep this caption", []string{"travel", "mountain"}, "2024-02-29"
	b.request("PATCH", "/api/photos/"+first, edit(p, private.ID), 200)
	b.request("POST", "/api/photos/"+archived+"/archive", nil, 200)
	b.publish("succeeded")
	live := b.request("GET", "/media/index.json", nil, 200)
	b.request("GET", "/api/catalog", nil, 200)
	before := b.state.Catalog
	for _, test := range []struct {
		ids    []string
		status int
	}{
		{[]string{}, 400},
		{[]string{first, first}, 400},
		{[]string{first, "missing"}, 404},
		{[]string{first, archived}, 409},
	} {
		b.request("POST", "/api/photostream", map[string]any{"photoIds": test.ids}, test.status)
		b.request("GET", "/api/catalog", nil, 200)
		if !reflect.DeepEqual(before, b.state.Catalog) {
			t.Fatal("Rejected selection partially changed the catalog")
		}
	}
	etag := b.state.ETag
	b.state.ETag = "stale"
	b.request("POST", "/api/photostream", map[string]any{"photoIds": []string{first, second}}, 409)
	b.state.ETag = etag
	b.async("POST", "/api/photostream", map[string]any{"photoIds": []string{first, second}})
	b.waitOperation("succeeded")
	b.request("GET", "/api/catalog", nil, 200)
	for i, photo := range b.state.Catalog.Photos {
		want := before.Photos[i]
		if want.ID == first || want.ID == second {
			want.Published = true
		}
		if !reflect.DeepEqual(want, photo) {
			t.Fatalf("Selection overwrote metadata or changed an unselected file: got %+v, want %+v", photo, want)
		}
	}
	if b.state.Catalog.Revision != before.Revision+1 || !reflect.DeepEqual(before.Albums, b.state.Catalog.Albums) {
		t.Fatal("Selection should save once without changing album membership or access")
	}
	if !bytes.Equal(live, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Saving the selection published it without confirmation")
	}
	b.publish("succeeded")
	var published catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &published)
	if len(published.Photos) != 2 || published.Photos[0].ID != second || published.Photos[1].ID != first {
		t.Fatalf("Publication did not contain exactly the selected files in newest-first order: %+v", published.Photos)
	}
}

type failingStorage struct {
	storage
	fail    bool
	failure func(method, bucket, key string, body []byte) bool
}

func (s *failingStorage) Copy(ctx context.Context, source, destination, key, contentType, cache string) error {
	if (s.fail && destination == "public" && strings.HasSuffix(key, "/1280.jpg")) || (s.failure != nil && s.failure("COPY", destination, key, nil)) {
		return errors.New("simulated storage outage")
	}
	return s.storage.Copy(ctx, source, destination, key, contentType, cache)
}

func (s *failingStorage) Write(ctx context.Context, bucket, key string, body []byte, contentType, cache string, expected *string) error {
	if (s.fail && bucket == "public" && strings.HasSuffix(key, "/1280.jpg")) || (s.failure != nil && s.failure("PUT", bucket, key, body)) {
		return errors.New("simulated storage outage")
	}
	return s.storage.Write(ctx, bucket, key, body, contentType, cache, expected)
}

func (s *failingStorage) Delete(ctx context.Context, bucket, key string) error {
	if s.failure != nil && s.failure("DELETE", bucket, key, nil) {
		return errors.New("simulated storage outage")
	}
	return s.storage.Delete(ctx, bucket, key)
}

func (s *failingStorage) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	if s.failure != nil && s.failure("LIST", bucket, prefix, nil) {
		return nil, errors.New("simulated storage outage")
	}
	return s.storage.List(ctx, bucket, prefix)
}

func TestArchiveRetainsFilesRedactsEveryManifestAndRestoresAlbumReadiness(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	archived, kept, draft := b.state.Catalog.Photos[0], b.state.Catalog.Photos[1], b.state.Catalog.Photos[2]
	for _, p := range []photo{archived, kept} {
		p.Alt, p.Published = "Published text", true
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	}
	b.request("POST", "/api/albums", map[string]string{"title": "Ordered album"}, 200)
	item := b.state.Catalog.Albums[0]
	order := []string{draft.ID, archived.ID, kept.ID}
	b.request("PUT", "/api/albums/"+item.ID, map[string]any{"title": item.Title, "slug": "ordered", "published": true, "photoIds": order[1:], "coverId": archived.ID}, 200)
	for range 2 {
		b.publish("succeeded")
	}
	// Archive must not accidentally publish this ready-but-unpublished draft.
	draft.Alt, draft.Published = "Not public yet", true
	b.request("PATCH", "/api/photos/"+draft.ID, edit(draft, item.ID), 200)
	b.request("PUT", "/api/albums/"+item.ID, map[string]any{"title": item.Title, "slug": "ordered", "published": true, "photoIds": order, "coverId": archived.ID}, 200)
	before := map[string]string{}
	for _, bucket := range []string{"private", "public"} {
		keys, err := store.List(t.Context(), bucket, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if strings.HasSuffix(key, ".json") {
				continue
			}
			value, err := store.Read(t.Context(), bucket, key)
			if err != nil {
				t.Fatal(err)
			}
			before[bucket+"/"+key] = value.ETag
		}
	}
	stale := b.state.ETag
	b.request("POST", "/api/photos/"+archived.ID+"/archive", nil, 200)
	current := b.state.ETag
	b.state.ETag = stale
	b.request("POST", "/api/photos/"+kept.ID+"/archive", nil, 409)
	b.state.ETag = current
	b.request("POST", "/api/photos/missing/archive", nil, 404)
	if p := b.state.Catalog.Photos[0]; !p.Archived || p.Archiving || p.Published || p.Filename != archived.Filename {
		t.Fatalf("Archive did not retain metadata with completed state: %+v", p)
	}
	if !reflect.DeepEqual(b.state.Catalog.Albums[0].PhotoIDs, order) || b.state.Catalog.Albums[0].CoverID != archived.ID {
		t.Fatal("Archive lost private album order/cover")
	}
	keys, err := store.List(t.Context(), "public", "releases/")
	if err != nil || len(keys) != 2 {
		t.Fatalf("Historical releases: %v %v", keys, err)
	}
	for _, key := range append(keys, "index.json") {
		value, err := store.Read(t.Context(), "public", key)
		if err != nil {
			t.Fatal(err)
		}
		var live catalog
		if err := json.Unmarshal(value.Body, &live); err != nil {
			t.Fatal(err)
		}
		if len(live.Photos) != 1 || live.Photos[0].ID != kept.ID || live.Photos[0].Alt != "Published text" || len(live.Albums) != 1 || !reflect.DeepEqual(live.Albums[0].PhotoIDs, []string{kept.ID}) || live.Albums[0].CoverID != kept.ID {
			t.Fatalf("Wrong redacted manifest %s: %+v", key, live)
		}
	}
	for path, etag := range before {
		bucket, key, _ := strings.Cut(path, "/")
		value, err := store.Read(t.Context(), bucket, key)
		if err != nil || value == nil || value.ETag != etag {
			t.Fatalf("Archive altered file %s: %v", path, err)
		}
	}
	b.request("PATCH", "/api/photos/"+archived.ID, edit(archived), 409)
	b.publish("succeeded")
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	for _, p := range live.Photos {
		if p.ID == archived.ID {
			t.Fatal("Publish resurrected archived photo")
		}
	}
	b = newBrowser(t, store)
	beforeRestore := b.request("GET", "/media/index.json", nil, 200)
	b.request("POST", "/api/photos/"+archived.ID+"/restore", nil, 200)
	if p := b.state.Catalog.Photos[0]; p.Archived || p.Published {
		t.Fatalf("Restore must clear archive without enabling independent publication: %+v", p)
	}
	if !bytes.Equal(beforeRestore, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Restore changed the public index without publishing")
	}
	b.request("POST", "/api/photos/"+kept.ID+"/restore", nil, 400)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 3 || !reflect.DeepEqual(live.Albums[0].PhotoIDs, order) {
		t.Fatalf("Restored photo must inherit published album readiness: %+v", live)
	}
}

func TestArchiveRetriesHistoricalCleanupAfterRestart(t *testing.T) {
	for _, failedKey := range []string{"listing", "index.json", "releases/", "catalog.json"} {
		t.Run(failedKey, func(t *testing.T) {
			store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
			b := newBrowser(t, store)
			b.raw("POST", "/api/photos", smallPNG(t), 200)
			p := b.state.Catalog.Photos[0]
			p.Alt, p.Published = "Keep my files", true
			b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
			b.publish("succeeded")
			store.failure = func(method, bucket, key string, body []byte) bool {
				if failedKey == "listing" {
					return method == "LIST"
				}
				if method != "PUT" {
					return false
				}
				if failedKey == "catalog.json" {
					var c catalog
					json.Unmarshal(body, &c)
					return bucket == "private" && key == failedKey && len(c.Photos) == 1 && c.Photos[0].Archived && !c.Photos[0].Archiving
				}
				return bucket == "public" && strings.HasPrefix(key, failedKey)
			}
			b.request("POST", "/api/photos/"+p.ID+"/archive", nil, 500)
			b = newBrowser(t, store)
			if !b.state.Catalog.Photos[0].Archiving {
				t.Fatal("Lost pending archive across restart")
			}
			b.request("POST", "/api/photos/"+p.ID+"/restore", nil, 409)
			b.request("PATCH", "/api/photos/"+p.ID, edit(p), 409)
			store.failure = nil
			b.request("POST", "/api/photos/"+p.ID+"/archive", nil, 200)
			if b.state.Catalog.Photos[0].Archiving {
				t.Fatal("Retry did not complete")
			}
			keys, _ := store.List(t.Context(), "public", "releases/")
			for _, key := range append(keys, "index.json") {
				value, err := store.Read(t.Context(), "public", key)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(value.Body, []byte(p.ID)) {
					t.Fatalf("Retry left reference in %s", key)
				}
			}
			b.request("GET", "/api/photos/"+p.ID+"/image", nil, 200)
		})
	}
}

func TestDeletePhotoRemovesFilesAndMembershipsWithoutPublishingDrafts(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	deleted, kept, draft := b.state.Catalog.Photos[0], b.state.Catalog.Photos[1], b.state.Catalog.Photos[2]
	for _, p := range []photo{deleted, kept} {
		p.Alt, p.Published = "Original published caption", true
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	}
	for i, ids := range [][]string{{deleted.ID, kept.ID}, {kept.ID, deleted.ID}, {deleted.ID}} {
		b.request("POST", "/api/albums", map[string]string{"title": fmt.Sprintf("Album %d", i)}, 200)
		item := b.state.Catalog.Albums[i]
		cover := deleted.ID
		if i == 1 {
			cover = kept.ID
		}
		b.request("PUT", "/api/albums/"+item.ID, map[string]any{"title": item.Title, "slug": fmt.Sprintf("album-%d", i), "published": true, "photoIds": ids, "coverId": cover}, 200)
	}
	b.publish("succeeded")
	// These ready-but-unpublished edits must NOT be published by deletion.
	kept.Alt, kept.Published = "Unpublished change", true
	b.request("PATCH", "/api/photos/"+kept.ID, edit(kept, b.state.Catalog.Albums[0].ID, b.state.Catalog.Albums[1].ID), 200)
	draft.Alt, draft.Published = "Still not live", true
	b.request("PATCH", "/api/photos/"+draft.ID, edit(draft, b.state.Catalog.Albums[0].ID), 200)
	stale := b.state.ETag
	b.request("POST", "/api/albums", map[string]string{"title": "Unpublished album"}, 200)
	current := b.state.ETag
	b.state.ETag = stale
	b.request("DELETE", "/api/photos/"+deleted.ID, nil, 409)
	b.state.ETag = current
	if value, err := store.Read(t.Context(), "private", "originals/"+deleted.ID); err != nil || value == nil {
		t.Fatalf("Stale delete touched the original: %v", err)
	}
	b.request("DELETE", "/api/photos/"+strings.Repeat("0", 32), nil, 404)
	b.request("DELETE", "/api/photos/"+deleted.ID, nil, 200)
	if len(b.state.Catalog.Photos) != 2 || b.state.Catalog.Photos[0].ID != kept.ID || b.state.Catalog.Photos[1].ID != draft.ID {
		t.Fatalf("Deletion affected unrelated library photos: %+v", b.state.Catalog.Photos)
	}
	for i, expected := range [][]string{{kept.ID, draft.ID}, {kept.ID}, {}, {}} {
		item := b.state.Catalog.Albums[i]
		if !reflect.DeepEqual(item.PhotoIDs, expected) || (i < 2 && item.CoverID != expected[0]) || (i >= 2 && item.CoverID != "") {
			t.Fatalf("Wrong private album membership/cover: %+v", item)
		}
	}
	var live catalog
	if err := json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Photos) != 1 || live.Photos[0].ID != kept.ID || live.Photos[0].Alt != "Original published caption" || len(live.Albums) != 3 {
		t.Fatalf("Deletion published unrelated drafts or retained deleted photo: %+v", live)
	}
	for i, item := range live.Albums {
		expected, cover := []string{}, ""
		if i < 2 {
			expected, cover = []string{kept.ID}, kept.ID
		}
		if !reflect.DeepEqual(item.PhotoIDs, expected) || item.CoverID != cover {
			t.Fatalf("Wrong live album membership/cover: %+v", item)
		}
	}
	for _, bucket := range []string{"private", "public"} {
		for _, size := range []int{640, 1280, 2560} {
			key := fmt.Sprintf("photos/%s/%d.jpg", deleted.ID, size)
			if value, err := store.Read(t.Context(), bucket, key); err != nil || value != nil {
				t.Fatalf("Deleted image still stored: %s/%s: %v", bucket, key, err)
			}
			key = fmt.Sprintf("photos/%s/%d.jpg", kept.ID, size)
			if value, err := store.Read(t.Context(), bucket, key); err != nil || value == nil {
				t.Fatalf("Unrelated image lost: %s/%s: %v", bucket, key, err)
			}
		}
	}
	if value, err := store.Read(t.Context(), "private", "originals/"+deleted.ID); err != nil || value != nil {
		t.Fatalf("Original not deleted: %v", err)
	}
	b.request("GET", "/api/photos/"+deleted.ID+"/image", nil, 404)
	b.request("GET", "/media/photos/"+deleted.ID+"/2560.jpg", nil, 404)
	// An unpublished photo is deletable too, including with no public derivative.
	b.request("DELETE", "/api/photos/"+draft.ID, nil, 200)
	b.request("DELETE", "/api/photos/"+deleted.ID, nil, 404)
}

func TestDeletePhotoResumesAfterStorageFailureAndRestart(t *testing.T) {
	for _, stage := range []string{"intent", "index", "public image", "private image", "original", "final catalog"} {
		t.Run(stage, func(t *testing.T) {
			store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
			b := newBrowser(t, store)
			b.raw("POST", "/api/photos", smallPNG(t), 200)
			p := b.state.Catalog.Photos[0]
			p.Alt, p.Published = "Delete this synthetic photo", true
			b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
			b.publish("succeeded")
			store.failure = func(method, bucket, key string, body []byte) bool {
				switch stage {
				case "intent":
					return method == "PUT" && bucket == "private" && key == "catalog.json"
				case "index":
					return method == "PUT" && bucket == "public" && key == "index.json"
				case "public image", "private image":
					return method == "DELETE" && bucket == strings.Fields(stage)[0] && key == "photos/"+p.ID+"/1280.jpg"
				case "original":
					return method == "DELETE" && key == "originals/"+p.ID
				case "final catalog":
					return method == "PUT" && bucket == "private" && key == "catalog.json" && !bytes.Contains(body, []byte(`"deleting": true`))
				}
				return false
			}
			b.request("DELETE", "/api/photos/"+p.ID, nil, 500)
			b.server.Close()
			b = newBrowser(t, store)
			if len(b.state.Catalog.Photos) != 1 || b.state.Catalog.Photos[0].Deleting != (stage != "intent") {
				t.Fatalf("Lost deletion recovery state: %+v", b.state.Catalog.Photos)
			}
			if stage == "intent" || stage == "index" {
				b.request("GET", "/media/photos/"+p.ID+"/640.jpg", nil, 200)
			}
			store.failure = nil
			if stage != "intent" {
				b.request("PATCH", "/api/photos/"+p.ID, edit(p), 409)
				b.publish("succeeded")
				var live catalog
				json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
				if len(live.Photos) != 0 {
					t.Fatal("Publishing resurrected a partially deleted photo")
				}
			}
			b.request("DELETE", "/api/photos/"+p.ID, nil, 200)
			if len(b.state.Catalog.Photos) != 0 {
				t.Fatal("Retry did not finish deletion")
			}
			for _, bucket := range []string{"private", "public"} {
				for _, key := range []string{"photos/" + p.ID + "/640.jpg", "photos/" + p.ID + "/1280.jpg", "photos/" + p.ID + "/2560.jpg"} {
					if value, err := store.Read(t.Context(), bucket, key); err != nil || value != nil {
						t.Fatalf("Retry left image %s/%s: %v", bucket, key, err)
					}
				}
			}
			if value, err := store.Read(t.Context(), "private", "originals/"+p.ID); err != nil || value != nil {
				t.Fatalf("Retry left original: %v", err)
			}
		})
	}
}

func TestFailedPublicationDoesNotReplaceLiveIndex(t *testing.T) {
	store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt = "Initial photo"
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.publish("succeeded")
	before := b.request("GET", "/media/index.json", nil, 200)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p = b.state.Catalog.Photos[1]
	p.Alt = "New photo"
	p.Published = true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	store.fail = true
	b.publish("failed")
	if !bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Failed image upload replaced live index")
	}
	store.fail = false
	b.publish("succeeded")
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
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
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
	b.request("GET", "/api/publication", nil, 401)
	b.request("GET", "/api/operation", nil, 401)
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

func TestR2PreviewRedirects(t *testing.T) {
	const imageID = "11111111111111111111111111111111"
	const videoID = "22222222222222222222222222222222"
	const deletingID = "33333333333333333333333333333333"
	body, err := encode(catalog{Version: 4, Photos: []photo{
		{ID: imageID}, {ID: videoID, Kind: "video"}, {ID: deletingID, Deleting: true},
	}, Albums: []album{}})
	if err != nil {
		t.Fatal(err)
	}
	r2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/private/catalog.json" {
			t.Errorf("Studio requested media bytes or mutated storage: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("ETag", `"catalog-etag"`)
		w.Write(body)
	}))
	defer r2.Close()
	a := &app{store: newR2Storage(r2.URL, "disposable-key", "disposable-secret", "private", "public"), config: configuration{AuthMode: "proxy", AllowedEmail: "admin@example.invalid", UploadOrigin: r2.URL}}
	handler := a.routes()
	for _, test := range []struct {
		method, id, suffix, identity, key string
		status                            int
	}{
		{"GET", imageID, "image?size=640", "admin@example.invalid", imageID + "/640.jpg", 307},
		{"GET", imageID, "image", "admin@example.invalid", imageID + "/1280.jpg", 307},
		{"HEAD", imageID, "image?size=1280", "admin@example.invalid", imageID + "/1280.jpg", 307},
		{"GET", videoID, "image?size=640", "admin@example.invalid", videoID + "/640.jpg", 307},
		{"GET", videoID, "video", "admin@example.invalid", videoID + "/video.mp4", 307},
		{"HEAD", videoID, "video", "admin@example.invalid", videoID + "/video.mp4", 307},
		{"GET", imageID, "video", "admin@example.invalid", "", 404},
		{"GET", deletingID, "image", "admin@example.invalid", "", 404},
		{"GET", strings.Repeat("a", 32), "image", "admin@example.invalid", "", 404},
		{"GET", imageID, "image?size=../catalog.json", "admin@example.invalid", "", 400},
		{"GET", imageID, "image?size=2560", "admin@example.invalid", "", 400},
		{"GET", "originals", "image", "admin@example.invalid", "", 404},
		{"GET", imageID, "image", "", "", 401},
		{"GET", videoID, "video", "other@example.invalid", "", 401},
		{"HEAD", videoID, "video", "", "", 401},
	} {
		t.Run(test.method+"/"+test.id+"/"+test.suffix+"/"+test.identity, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "/api/photos/"+test.id+"/"+test.suffix, nil)
			r.Header.Set("X-Auth-Request-Email", test.identity)
			r.Header.Set("Range", "bytes=17-53")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Status/cache: %d %v", w.Code, w.Header())
			}
			location, err := w.Result().Location()
			if test.status != 307 {
				if err == nil {
					t.Fatal("Rejected preview issued a signed URL")
				}
				return
			}
			if err != nil || location.Scheme+"://"+location.Host != r2.URL || location.Path != "/private/photos/"+test.key {
				t.Fatalf("Incorrect signed destination: %v %v", location, err)
			}
			query := location.Query()
			if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || query.Get("X-Amz-Expires") != "900" || len(query.Get("X-Amz-Signature")) != 64 || !strings.HasPrefix(query.Get("X-Amz-Credential"), "disposable-key/") || query.Get("X-Amz-SignedHeaders") != "host" {
				t.Fatal("Missing signature, wrong expiry, or range/cookie-bound signature")
			}
			// Verify the returned capability for the actual HTTP method, not just
			// the presence of a signature (a GET signature cannot authorize HEAD).
			signature := query.Get("X-Amz-Signature")
			signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
			if err != nil {
				t.Fatal(err)
			}
			query.Del("X-Amz-Signature")
			location.RawQuery = query.Encode()
			verification, _ := http.NewRequest(test.method, location.String(), nil)
			verified, _, err := v4.NewSigner().PresignHTTP(t.Context(), aws.Credentials{AccessKeyID: "disposable-key", SecretAccessKey: "disposable-secret"}, verification, "UNSIGNED-PAYLOAD", "s3", "auto", signedAt)
			if err != nil || !strings.Contains(verified, "X-Amz-Signature="+signature) {
				t.Fatalf("Invalid %s signature: %v", test.method, err)
			}
			if test.method == "GET" && query.Get("response-cache-control") != "private, no-store" {
				t.Fatal("Signed media allowed caching")
			}
			if w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "media-src 'self' "+r2.URL+";") {
				t.Fatal("Redirect referrer or direct video CSP is incorrect")
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "img-src 'self' https: blob: data:;") {
				t.Fatal("Studio CSP blocks generated BlurHash previews")
			}
		})
	}
}
