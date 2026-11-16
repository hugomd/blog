package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Hold actual image copies, not the HTTP handler, to exercise a slow bucket.
type pausedCopyStorage struct {
	storage
	paused  bool
	started chan string
	release chan struct{}
}

func (s *pausedCopyStorage) Copy(ctx context.Context, source, destination, key, contentType, cache string) error {
	if s.paused {
		select {
		case s.started <- key:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.storage.Copy(ctx, source, destination, key, contentType, cache)
}

func TestPublicationRunsOutsideRequestAndResumesAfterRestart(t *testing.T) {
	store := &pausedCopyStorage{storage: &localStorage{root: t.TempDir()}, started: make(chan string, 6), release: make(chan struct{})}
	b := newBrowser(t, store)
	for i := range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
		p := b.state.Catalog.Photos[i]
		p.Alt, p.Published = "A quiet street", true
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
		if i == 0 {
			b.publish("succeeded")
		}
	}
	before := b.request("GET", "/media/index.json", nil, 200)
	store.paused = true
	b.client.Timeout = 2 * time.Second
	accepted := b.request("POST", "/api/publish", nil, http.StatusAccepted)
	var first struct{ Publication publication }
	if err := json.Unmarshal(accepted, &first); err != nil {
		t.Fatal(err)
	}
	// Two independently blocked copies prove that copying is concurrent.
	for range 2 {
		select {
		case <-store.started:
		case <-time.After(5 * time.Second):
			t.Fatal("Image copies did not start independently of the completed request")
		}
	}
	b.request("GET", "/api/catalog", nil, 200)
	var duplicate struct{ Publication publication }
	json.Unmarshal(b.request("POST", "/api/publish", nil, http.StatusAccepted), &duplicate)
	if duplicate.Publication.ID != first.Publication.ID || duplicate.Publication.State != "running" || duplicate.Publication.Total != 6 {
		t.Fatalf("Duplicate request did not return the running six-image job: %+v", duplicate)
	}
	id := b.state.Catalog.Photos[0].ID
	b.request("PATCH", "/api/photos/"+id, edit(b.state.Catalog.Photos[0]), 423)
	b.request("DELETE", "/api/photos/"+id, nil, 423)
	b.request("POST", "/api/photos/"+id+"/archive", nil, 423)
	if !bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Incomplete copies changed the discoverable gallery")
	}
	b.stop()
	// A cancelled process leaves a durable job, not a failed publication.
	record, err := store.Read(t.Context(), "private", "publication.json")
	if err != nil || record == nil || !bytes.Contains(record.Body, []byte(`"state": "running"`)) {
		t.Fatalf("No resumable publication record: %v", err)
	}
	store.paused = false
	b = newBrowser(t, store)
	job := b.waitPublication("succeeded")
	if job.ID != first.Publication.ID || job.Completed != 6 || job.Total != 6 {
		t.Fatalf("Restart did not finish the original job: %+v", job)
	}
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 3 || live.Revision != b.state.Catalog.Revision {
		t.Fatalf("Wrong final publication: %+v", live)
	}
	for _, p := range live.Photos {
		for _, size := range []string{"640", "1280", "2560"} {
			key := "photos/" + p.ID + "/" + size + ".jpg"
			private, err := store.Read(t.Context(), "private", key)
			if err != nil || private == nil {
				t.Fatalf("Private source missing: %s: %v", key, err)
			}
			public := b.request("GET", "/media/"+key, nil, 200)
			if !bytes.Equal(private.Body, public) {
				t.Fatalf("Copy changed image bytes: %s", key)
			}
		}
	}
}

func TestPublicationStoresImageRSSAndKeepsItInSync(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	c := catalog{Version: 4, Photos: []photo{}, Albums: []album{}}
	for i := range 7 {
		c.Photos = append(c.Photos, photo{ID: strings.Repeat(fmt.Sprint(i+1), 32), Alt: fmt.Sprintf("Photo %d", i), UploadedAt: fmt.Sprintf("2026-11-%02dT03:04:05Z", i+1)})
	}
	c.Photos[0].Published, c.Photos[1].Published = true, true
	c.Photos[0].Alt, c.Photos[0].Caption = `City "lights" & <people>`, "<script>literal text</script> & ]]> café"
	c.Photos[0].CapturedDate = "2020-01-01"
	c.Photos[1].Alt, c.Photos[1].CapturedDate = "", "2024-02-03"
	c.Photos[2].Caption = "PRIVATE PHOTO CAPTION"
	c.Photos[3].Caption = "DRAFT CAPTION"
	c.Photos[4].Published, c.Photos[4].Archived = true, true
	c.Photos[5].Published, c.Photos[5].Kind = true, "video"
	token := strings.Repeat("a", 64)
	c.Albums = []album{
		{ID: identifier(), Title: "PUBLIC ALBUM", Slug: "public", Published: true, PhotoIDs: []string{c.Photos[6].ID}},
		{ID: identifier(), Title: "PRIVATE ALBUM", Slug: "private", Published: true, Access: "link", LinkToken: token, LinkHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), AccessVersion: "v1", PhotoIDs: []string{c.Photos[2].ID, c.Photos[6].ID}},
	}
	c.PhotostreamOrder = []string{c.Photos[0].ID, c.Photos[6].ID, c.Photos[1].ID, c.Photos[5].ID}
	fixtureCatalog(t, store, "private", "catalog.json", c)
	_, _, _, _, variants, err := processImage(smallPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Photos {
		for _, key := range p.files() {
			if err := store.Write(t.Context(), "private", key, variants[0].Body, "image/jpeg", "no-store", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	b := newBrowser(t, store)
	b.publish("succeeded")
	type item struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		GUID        string `xml:"guid"`
		PubDate     string `xml:"pubDate"`
		Description string `xml:"description"`
	}
	read := func(ids ...string) []item {
		t.Helper()
		body := b.request("GET", "/media/index.xml", nil, 200)
		var feed struct {
			Version string `xml:"version,attr"`
			Channel struct {
				Self struct {
					Href string `xml:"href,attr"`
					Rel  string `xml:"rel,attr"`
					Type string `xml:"type,attr"`
				} `xml:"http://www.w3.org/2005/Atom link"`
				Title         string `xml:"title"`
				Link          string `xml:"link"`
				Description   string `xml:"description"`
				Language      string `xml:"language"`
				LastBuildDate string `xml:"lastBuildDate"`
				Items         []item `xml:"item"`
			} `xml:"channel"`
		}
		if err := xml.Unmarshal(body, &feed); err != nil {
			t.Fatal(err)
		}
		if feed.Version != "2.0" || feed.Channel.Self.Href != "/media/index.xml" || feed.Channel.Self.Rel != "self" || feed.Channel.Self.Type != "application/rss+xml" || len(feed.Channel.Items) != len(ids) {
			t.Fatalf("Invalid RSS channel or items: %s", body)
		}
		if feed.Channel.Link != "https://example.test/photos/" || feed.Channel.Title != "Photostream :: Hugo’s Blog" || feed.Channel.Description == "" || feed.Channel.Language != "en-us" {
			t.Fatalf("Missing channel metadata: %s", body)
		}
		if _, err := time.Parse(time.RFC1123Z, feed.Channel.LastBuildDate); err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			if got := feed.Channel.Items[i]; got.Link != "https://example.test/photos/"+id+"/" || got.GUID != got.Link {
				t.Fatalf("Wrong order or unstable viewer link: %+v", got)
			}
		}
		for _, secret := range []string{"PRIVATE PHOTO CAPTION", "DRAFT CAPTION", "PRIVATE ALBUM", "video.mp4", c.Photos[2].ID, c.Photos[3].ID, c.Photos[4].ID, c.Photos[5].ID} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatalf("RSS exposed excluded metadata: %s", secret)
			}
		}
		return feed.Channel.Items
	}
	items := read(c.Photos[0].ID, c.Photos[6].ID, c.Photos[1].ID)
	wantDescription := `<a href="https://example.test/photos/` + c.Photos[0].ID + `/"><img src="/media/photos/` + c.Photos[0].ID + `/2560.jpg" alt="City &#34;lights&#34; &amp; &lt;people&gt;"></a><p>&lt;script&gt;literal text&lt;/script&gt; &amp; ]]&gt; café</p>`
	if items[0].Title != c.Photos[0].Alt || items[0].Description != wantDescription || items[0].PubDate != "Sun, 01 Nov 2026 03:04:05 +0000" || items[2].Title != "2024-02-03" {
		t.Fatalf("Wrong image, escaping, date or fallback title: %+v", items)
	}
	// Draft changes must not enter the feed during unrelated archive/delete.
	p := b.state.Catalog.Photos[0]
	p.Caption = "UNPUBLISHED CAPTION"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/photos/"+c.Photos[1].ID+"/archive", nil, 200)
	if got := read(c.Photos[0].ID, c.Photos[6].ID); got[0].Description != wantDescription {
		t.Fatal("Archiving published unrelated working edits")
	}
	b.request("DELETE", "/api/photos/"+c.Photos[6].ID, nil, 200)
	read(c.Photos[0].ID)
	p.Published = false
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.publish("succeeded")
	read() // A video-only photostream still produces a valid, empty image feed.
}

func TestRSSWriteFailureCanBeRetriedAfterCatalogCommit(t *testing.T) {
	for _, action := range []string{"publish", "archive", "delete"} {
		t.Run(action, func(t *testing.T) {
			store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
			b := newBrowser(t, store)
			b.raw("POST", "/api/photos", smallPNG(t), 200)
			p := b.state.Catalog.Photos[0]
			p.Published, p.Alt = true, "Published image"
			b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
			if action != "publish" {
				b.publish("succeeded")
			}
			store.failure = func(method, bucket, key string, _ []byte) bool {
				return method == "PUT" && bucket == "public" && key == "index.xml"
			}
			run := func(failed bool) {
				if action == "publish" {
					state := "succeeded"
					if failed {
						state = "failed"
					}
					b.publish(state)
					return
				}
				method, path, status := "DELETE", "/api/photos/"+p.ID, 200
				if action == "archive" {
					method, path = "POST", path+"/archive"
				}
				if failed {
					status = 500
				}
				b.request(method, path, nil, status)
			}
			run(true)
			var live catalog
			if err := json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live); err != nil || (len(live.Photos) == 1) != (action == "publish") {
				t.Fatalf("Expected catalog commit before feed failure: %+v %v", live, err)
			}
			b.stop()
			store.failure = nil
			b = newBrowser(t, store)
			run(false)
			body := b.request("GET", "/media/index.xml", nil, 200)
			if bytes.Contains(body, []byte(p.ID)) != (action == "publish") {
				t.Fatalf("Retry left stale RSS: %s", body)
			}
		})
	}
}

func TestBackgroundPublicationPreservesConflictingLiveIndex(t *testing.T) {
	store := &pausedCopyStorage{storage: &localStorage{root: t.TempDir()}, paused: true, started: make(chan string, 3), release: make(chan struct{})}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt, p.Published = "Private source", true
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/publish", nil, http.StatusAccepted)
	select {
	case <-store.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Copy did not start")
	}
	external := []byte(`{"version":1,"revision":99,"photos":[],"albums":[]}`)
	if err := store.Write(t.Context(), "public", "index.json", external, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	close(store.release)
	b.waitPublication("failed")
	if !bytes.Equal(external, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Publication overwrote an independently changed live index")
	}
	b.request("GET", "/media/index.xml", nil, 404)
}

func TestResumedPublicationRejectsChangedCatalog(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	b.stop()
	// Simulate a different catalog being restored while the service is stopped.
	body, _ := encode(publication{ID: "interrupted", Revision: 0, CatalogETag: "old-catalog", State: "running"})
	if err := store.Write(context.Background(), "private", "publication.json", body, "application/json", "no-store", nil); err != nil {
		t.Fatal(err)
	}
	b = newBrowser(t, store)
	job := b.waitPublication("failed")
	if job.ID != "interrupted" || job.Error == "" {
		t.Fatalf("Changed source was not rejected: %+v", job)
	}
	b.request("GET", "/media/index.json", nil, 404)
}

func TestPublicationChangesCompareLiveContent(t *testing.T) {
	b := newBrowser(t, &localStorage{root: t.TempDir()})
	b.request("POST", "/api/publish?republish=1", nil, http.StatusConflict)
	type counts struct{ Live, Next, Added, Updated, Removed, Unchanged int }
	expect := func(changed bool, photos, albums counts, response ...[]byte) {
		t.Helper()
		if len(response) == 0 {
			response = append(response, b.request("GET", "/api/catalog", nil, 200))
		}
		var result struct {
			Changes *struct {
				Changed bool
				Photos  counts
				Albums  counts
			} `json:"publicationChanges"`
		}
		if err := json.Unmarshal(response[0], &result); err != nil {
			t.Fatal(err)
		}
		if result.Changes == nil || result.Changes.Changed != changed || !reflect.DeepEqual(result.Changes.Photos, photos) || !reflect.DeepEqual(result.Changes.Albums, albums) {
			t.Fatalf("Got publication changes %+v; want changed=%v, photos=%+v, albums=%+v", result.Changes, changed, photos, albums)
		}
	}
	for range 5 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	kept, updated, removed, added := b.state.Catalog.Photos[0], b.state.Catalog.Photos[1], b.state.Catalog.Photos[2], b.state.Catalog.Photos[3]
	standalone := b.state.Catalog.Photos[4]
	for _, p := range []photo{kept, updated, removed} {
		p.Alt, p.Published = "A street", true
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	}
	b.request("POST", "/api/albums", map[string]string{"title": "Kept album"}, 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Removed album"}, 200)
	first, second := b.state.Catalog.Albums[0], b.state.Catalog.Albums[1]
	saveAlbum := func(a album, published bool, ids []string) {
		t.Helper()
		b.request("PUT", "/api/albums/"+a.ID, map[string]any{"title": a.Title, "slug": a.Slug, "description": a.Description, "published": published, "photoIds": ids, "coverId": ""}, 200)
	}
	saveAlbum(first, true, []string{kept.ID, updated.ID})
	saveAlbum(second, true, []string{removed.ID})
	// Before the first release, only ready content is new, not the private draft.
	expect(true, counts{Next: 3, Added: 3}, counts{Next: 2, Added: 2})
	b.publish("succeeded")
	expect(false, counts{Live: 3, Next: 3, Unchanged: 3}, counts{Live: 2, Next: 2, Unchanged: 2})

	// Private-only changes advance the revision, but do not change the website.
	added.Alt, added.Caption = "A new street", "Private draft caption"
	saved := b.request("PATCH", "/api/photos/"+added.ID, edit(added), 200)
	expect(false, counts{Live: 3, Next: 3, Unchanged: 3}, counts{Live: 2, Next: 2, Unchanged: 2}, saved)
	b.request("POST", "/api/albums", map[string]string{"title": "New draft album"}, 200)
	third := b.state.Catalog.Albums[2]
	expect(false, counts{Live: 3, Next: 3, Unchanged: 3}, counts{Live: 2, Next: 2, Unchanged: 2})

	updated = b.state.Catalog.Photos[1]
	updated.Caption = "Changed public caption"
	b.request("PATCH", "/api/photos/"+updated.ID, edit(updated, first.ID), 200)
	removed = b.state.Catalog.Photos[2]
	removed.Published = false
	b.request("PATCH", "/api/photos/"+removed.ID, edit(removed), 200)
	b.request("DELETE", "/api/albums/"+second.ID, nil, 200)
	b.request("PATCH", "/api/photos/"+added.ID, edit(added, first.ID), 200)
	standalone.Alt, standalone.Published = "A new standalone photo", true
	b.request("PATCH", "/api/photos/"+standalone.ID, edit(standalone), 200)
	saveAlbum(third, true, []string{added.ID, kept.ID})
	// The net gain of one photo hides two additions and one removal. Album
	// totals are unchanged despite changes. Inherited photos count only once.
	expect(true, counts{Live: 3, Next: 4, Added: 2, Updated: 1, Removed: 1, Unchanged: 1}, counts{Live: 2, Next: 2, Added: 1, Updated: 1, Removed: 1})
	b.publish("succeeded")
	expect(false, counts{Live: 4, Next: 4, Unchanged: 4}, counts{Live: 2, Next: 2, Unchanged: 2})

	// Switching from independent to inherited readiness is not a public edit.
	kept = b.state.Catalog.Photos[0]
	kept.Published = false
	saved = b.request("PATCH", "/api/photos/"+kept.ID, edit(kept, first.ID, third.ID), 200)
	expect(false, counts{Live: 4, Next: 4, Unchanged: 4}, counts{Live: 2, Next: 2, Unchanged: 2}, saved)

	// A curated order change updates the album, not the photos or their totals.
	saveAlbum(first, true, []string{added.ID, updated.ID, kept.ID})
	expect(true, counts{Live: 4, Next: 4, Unchanged: 4}, counts{Live: 2, Next: 2, Updated: 1, Unchanged: 1})
	b.publish("succeeded")
	// Archive already updates the live manifests; no publication remains due.
	saved = b.request("POST", "/api/photos/"+kept.ID+"/archive", nil, 200)
	expect(false, counts{Live: 3, Next: 3, Unchanged: 3}, counts{Live: 2, Next: 2, Unchanged: 2}, saved)
}

func TestPublicationBackfillsLegacyBlurHashesWithoutChangingDrafts(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	a := &app{store: store, config: configuration{PrivateAlbums: true, PublicURL: "/media"}}
	c := catalog{Version: 2, Revision: 7, Photos: []photo{}, Albums: []album{}}
	for i := range 5 {
		c.Photos = append(c.Photos, photo{ID: identifier(), Tags: []string{}, Exif: map[string]string{}, Published: i < 3, UploadedAt: "2026-01-01", Width: 53, Height: 29})
	}
	c.Albums = append(c.Albums, album{ID: identifier(), Title: "Private", Slug: "private", Published: true, Access: "password", PasswordHash: "fixture-only", AccessVersion: "v1", PhotoIDs: []string{c.Photos[0].ID, c.Photos[3].ID}, CoverID: c.Photos[3].ID})
	write := func(bucket, key string, value any) {
		t.Helper()
		body, err := encode(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Write(t.Context(), bucket, key, body, "application/json", "no-store", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Seed a previously valid publication with old, hash-less metadata. Missing
	// and corrupt private previews must not break metadata-only republication.
	live := c.publicRelease("/media")
	live.ProtectedRelease = identifier()
	write("private", "catalog.json", c)
	write("public", "index.json", live)
	write("protected", "releases/"+live.ProtectedRelease+".json", c.protectedRelease())
	_, _, _, _, variants, err := processImage(smallPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 2, 3} {
		body := variants[0].Body
		if i == 2 {
			body = []byte("invalid JPEG")
		}
		if err := store.Write(t.Context(), "private", "photos/"+c.Photos[i].ID+"/640.jpg", body, "image/jpeg", "no-store", nil); err != nil {
			t.Fatal(err)
		}
	}
	b := newBrowser(t, store)
	before, err := store.Read(t.Context(), "private", "catalog.json")
	if err != nil || before == nil {
		t.Fatal("Missing draft catalog")
	}
	check := func() {
		t.Helper()
		json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
		protected, _, err := a.loadProtected(t.Context(), live)
		if err != nil || len(live.Photos) != 3 || len(protected.Photos) != 2 {
			t.Fatalf("Wrong authorized projections: %+v, %v", protected, err)
		}
		for _, projection := range []catalog{live, protected} {
			for _, published := range projection.Photos {
				if published.ID == c.Photos[0].ID || published.ID == c.Photos[3].ID {
					decodePhotoBlurHash(t, published.BlurHash)
					if published.BlurHash != previewBlurHash(variants[0].Body) {
						t.Fatal("Legacy backfill did not describe the private preview")
					}
				} else {
					body, _ := encode(published)
					if published.BlurHash != "" || bytes.Contains(body, []byte(`"blurhash"`)) {
						t.Fatal("Missing/invalid preview should omit optional BlurHash")
					}
				}
			}
		}
		for _, published := range live.Photos {
			if published.ID == c.Photos[3].ID || published.ID == c.Photos[4].ID {
				t.Fatal("Private photo metadata escaped into public projection")
			}
		}
		after, err := store.Read(t.Context(), "private", "catalog.json")
		if err != nil || after == nil || before.ETag != after.ETag || !bytes.Equal(before.Body, after.Body) {
			t.Fatal("Derived metadata changed the durable job's draft catalog")
		}
		var response struct {
			Catalog catalog            `json:"catalog"`
			Changes publicationChanges `json:"publicationChanges"`
		}
		if err := json.Unmarshal(b.request("GET", "/api/catalog", nil, 200), &response); err != nil {
			t.Fatal(err)
		}
		for i, p := range response.Catalog.Photos {
			want := ""
			if i == 0 || i == 3 {
				want = previewBlurHash(variants[0].Body)
			}
			if p.BlurHash != want {
				t.Fatalf("Studio photo %d: got BlurHash %q, want %q", i, p.BlurHash, want)
			}
		}
		after, err = store.Read(t.Context(), "private", "catalog.json")
		if err != nil || after == nil || before.ETag != after.ETag || !bytes.Equal(before.Body, after.Body) {
			t.Fatal("Studio preview metadata changed the saved draft catalog")
		}
		if response.Changes.Changed || response.Changes.Photos.Unchanged != 3 || response.Changes.ProtectedPhotos.Unchanged != 2 {
			t.Fatalf("Hash-only differences left pending publication: %+v", response.Changes)
		}
	}
	b.request("POST", "/api/publish?republish=1", nil, http.StatusAccepted)
	b.waitPublication("succeeded")
	check()
	// Republish must reject both public edits and protected-only edits. Ordinary
	// publication still applies them deliberately and retains the derived hashes.
	for _, i := range []int{0, 3} {
		p := b.state.Catalog.Photos[i]
		p.Caption = "An ordinary metadata edit"
		b.request("PATCH", "/api/photos/"+p.ID, edit(p, c.Albums[0].ID), 200)
		liveBefore := b.request("GET", "/media/index.json", nil, 200)
		b.request("POST", "/api/publish?republish=1", nil, http.StatusConflict)
		if !bytes.Equal(liveBefore, b.request("GET", "/media/index.json", nil, 200)) {
			t.Fatal("Rejected republication changed the live manifests")
		}
		before, err = store.Read(t.Context(), "private", "catalog.json")
		if err != nil || before == nil {
			t.Fatal("Missing draft catalog")
		}
		b.publish("succeeded")
		check()
	}
	// The next ordinary publication reuses live hashes even when the old private
	// previews are no longer readable; media already live is not recopied.
	for _, i := range []int{0, 3} {
		if err := store.Delete(t.Context(), "private", "photos/"+c.Photos[i].ID+"/640.jpg"); err != nil {
			t.Fatal(err)
		}
	}
	b.publish("succeeded")
	check()
}
