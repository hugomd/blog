package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestProtectedAlbumsPublishSharedPhotosWithoutLeakingPrivateContent(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for range 4 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	photos := append([]photo{}, b.state.Catalog.Photos...)
	for i, p := range photos {
		p.Alt = []string{"Shared photograph", "Protected-only photograph", "Standalone public photograph", "Unpublished draft"}[i]
		p.Caption = p.Alt
		if i == 1 {
			p.Alt = "" // Private publication also permits optional alt text.
		}
		p.Published = i == 2
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	}
	b.request("POST", "/api/albums", map[string]string{"title": "Public collection"}, 200)
	publicID := b.state.Catalog.Albums[0].ID
	b.request("PUT", "/api/albums/"+publicID, map[string]any{"title": "Public collection", "slug": "public", "published": true, "photoIds": []string{photos[0].ID}, "coverId": photos[0].ID}, 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Secret collection"}, 200)
	privateID := b.state.Catalog.Albums[1].ID
	form := map[string]any{"title": "Secret collection", "slug": "secret", "description": "Private description", "published": true, "access": "password", "password": "a test album passphrase", "photoIds": []string{photos[1].ID, photos[0].ID}, "coverId": photos[1].ID}
	for _, password := range []string{"", strings.Repeat("x", 11), strings.Repeat("🔐", 129)} {
		form["password"] = password
		b.request("PUT", "/api/albums/"+privateID, form, 400)
	}
	form["password"] = strings.Repeat("🔐", 12)
	response := b.request("PUT", "/api/albums/"+privateID, form, 200)
	if bytes.Contains(response, []byte("passwordHash")) || bytes.Contains(response, []byte("accessVersion")) || bytes.Contains(response, []byte(form["password"].(string))) {
		t.Fatal("Admin response exposed stored password material")
	}
	if !b.state.Catalog.Albums[1].PasswordSet || b.state.Catalog.Version != 2 {
		t.Fatal("Missing password state or rollback-safe catalog version")
	}
	var before struct {
		Changes publicationChanges `json:"publicationChanges"`
	}
	json.Unmarshal(response, &before)
	if before.Changes.Photos.Next != 2 || before.Changes.Albums.Next != 1 || before.Changes.ProtectedPhotos.Next != 2 || before.Changes.ProtectedAlbums.Next != 1 {
		t.Fatalf("Incorrect audience counts: %+v", before.Changes)
	}
	b.publish("succeeded")
	var live catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	if len(live.Photos) != 2 || len(live.Albums) != 1 || live.Albums[0].ID != publicID || live.ProtectedRelease == "" {
		t.Fatalf("Incorrect public release: %+v", live)
	}
	protectedObject, err := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	if err != nil || protectedObject == nil {
		t.Fatalf("Missing protected release: %v", err)
	}
	var protected catalog
	json.Unmarshal(protectedObject.Body, &protected)
	if len(protected.Photos) != 2 || len(protected.Albums) != 1 || !reflect.DeepEqual(protected.Albums[0].PhotoIDs, []string{photos[1].ID, photos[0].ID}) {
		t.Fatalf("Incorrect protected release: %+v", protected)
	}
	if protected.Photos[0].ID != photos[1].ID || protected.Photos[0].Alt != "" {
		t.Fatal("Private publication must preserve an empty optional description")
	}
	credential := protected.Albums[0]
	if !strings.HasPrefix(credential.PasswordHash, "scrypt$16384$8$5$") || len(credential.AccessVersion) != 32 {
		t.Fatal("Missing salted password verifier or access version")
	}
	keys, _ := store.List(t.Context(), "public", "")
	for _, key := range keys {
		value, err := store.Read(t.Context(), "public", key)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{privateID, photos[1].ID, "Secret collection", "Private description", "Protected-only photograph", credential.PasswordHash, credential.AccessVersion, "a test album passphrase"} {
			if strings.Contains(key, secret) || bytes.Contains(value.Body, []byte(secret)) {
				t.Fatalf("Public object %s leaks protected information", key)
			}
		}
	}
	for _, p := range photos[:3] {
		for _, bucket := range []string{"public", "protected"} {
			value, err := store.Read(t.Context(), bucket, "photos/"+p.ID+"/640.jpg")
			if err != nil {
				t.Fatal(err)
			}
			want := p.ID == photos[0].ID || (bucket == "public" && p.ID == photos[2].ID) || (bucket == "protected" && p.ID == photos[1].ID)
			if (value != nil) != want {
				t.Fatalf("Incorrect delivery audience for %s in %s", p.ID, bucket)
			}
		}
	}
	if value, err := store.Read(t.Context(), "protected", "originals/"+photos[1].ID); value != nil || err != nil {
		t.Fatal("Protected delivery exposed an original")
	}
	b.request("GET", "/media/photos/"+photos[1].ID+"/640.jpg", nil, 404)
	delete(form, "password")
	b.request("PUT", "/api/albums/"+privateID, form, 200)
	var unchanged struct {
		Changes publicationChanges `json:"publicationChanges"`
	}
	json.Unmarshal(b.request("GET", "/api/catalog", nil, 200), &unchanged)
	if unchanged.Changes.Changed {
		t.Fatalf("Saving unchanged password should not require publication: %+v", unchanged.Changes)
	}
	form["password"] = "a replacement test passphrase"
	response = b.request("PUT", "/api/albums/"+privateID, form, 200)
	json.Unmarshal(response, &before)
	if !before.Changes.Changed || before.Changes.ProtectedAlbums.Updated != 1 || before.Changes.Albums.Updated != 0 {
		t.Fatal("Password rotation is not counted as a protected album update")
	}
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	rotated, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	json.Unmarshal(rotated.Body, &protected)
	if protected.Albums[0].PasswordHash == credential.PasswordHash || protected.Albums[0].AccessVersion == credential.AccessVersion {
		t.Fatal("Password rotation failed to invalidate prior sessions")
	}
	delete(form, "password")
	form["published"] = false
	b.request("PUT", "/api/albums/"+privateID, form, 200)
	b.publish("succeeded")
	form["published"] = true
	b.request("PUT", "/api/albums/"+privateID, form, 200)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	previousVersion := protected.Albums[0].AccessVersion
	republished, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	json.Unmarshal(republished.Body, &protected)
	if protected.Albums[0].AccessVersion == previousVersion {
		t.Fatal("Republishing revived revoked viewing sessions")
	}
	// Archive revokes image membership without publishing unrelated edits.
	b.request("POST", "/api/photos/"+photos[1].ID+"/archive", nil, 200)
	archived, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	if bytes.Contains(archived.Body, []byte(photos[1].ID)) {
		t.Fatal("Archive left protected photo metadata discoverable")
	}
	b.request("DELETE", "/api/photos/"+photos[0].ID, nil, 200)
	for _, bucket := range []string{"private", "public", "protected"} {
		value, err := store.Read(t.Context(), bucket, "photos/"+photos[0].ID+"/640.jpg")
		if err != nil || value != nil {
			t.Fatalf("Delete retained a shared derivative in %s", bucket)
		}
	}
}

func TestProtectedPublicationFailureRetainsBothLiveReleases(t *testing.T) {
	store := &failingStorage{storage: &localStorage{root: t.TempDir()}}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt = "Private photograph"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Protected"}, 200)
	id := b.state.Catalog.Albums[0].ID
	form := map[string]any{"title": "Protected", "slug": "protected", "published": true, "access": "password", "password": "initial test passphrase", "photoIds": []string{p.ID}}
	b.request("PUT", "/api/albums/"+id, form, 200)
	b.publish("succeeded")
	before := b.request("GET", "/media/index.json", nil, 200)
	var live catalog
	json.Unmarshal(before, &live)
	protected, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	form["password"] = "replacement test passphrase"
	b.request("PUT", "/api/albums/"+id, form, 200)
	for _, failedBucket := range []string{"protected", "public"} {
		store.failure = func(method, bucket, key string, _ []byte) bool {
			return method == "PUT" && bucket == failedBucket && (bucket == "protected" || key == "index.json")
		}
		b.publish("failed")
		if !bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
			t.Fatal("Failed publication changed its commit pointer")
		}
		current, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
		if !bytes.Equal(protected.Body, current.Body) {
			t.Fatal("Failed publication changed protected access before commit")
		}
	}
	store.failure = nil
	b.publish("succeeded")
	if bytes.Equal(before, b.request("GET", "/media/index.json", nil, 200)) {
		t.Fatal("Retry did not commit protected password changes")
	}
}

func TestMissingProtectedConfigurationRejectsWritesBeforeChangingStorage(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	b.raw("POST", "/api/photos", smallPNG(t), 200)
	p := b.state.Catalog.Photos[0]
	p.Alt = "Protected photo"
	b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Private"}, 200)
	id := b.state.Catalog.Albums[0].ID
	form := map[string]any{"title": "Private", "slug": "private", "published": true, "access": "password", "password": "test album passphrase", "photoIds": []string{p.ID}}
	b.request("PUT", "/api/albums/"+id, form, 200)
	b.publish("succeeded")
	b.stop()
	// Simulate a deployment losing its protected bucket configuration.
	a := &app{store: store, config: configuration{Development: true, AuthMode: "proxy", AllowedEmail: "admin@example.test", Origin: "https://studio.example.test"}, background: t.Context()}
	handler := a.routes()
	before := map[string]*object{}
	for _, bucket := range []string{"private", "public", "protected"} {
		keys, err := store.List(t.Context(), bucket, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			before[bucket+"/"+key], err = store.Read(t.Context(), bucket, key)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/api/photos", string(smallPNG(t))},
		{"POST", "/api/albums", `{"title":"Uncommitted"}`},
		{"PATCH", "/api/photos/" + p.ID, `{"alt":"Changed"}`},
		{"DELETE", "/api/albums/" + id, ""},
		{"POST", "/api/photos/" + p.ID + "/archive", ""},
		{"DELETE", "/api/photos/" + p.ID, ""},
		{"POST", "/api/publish", ""},
	} {
		r := httptest.NewRequest(request.method, "https://studio.example.test"+request.path, strings.NewReader(request.body))
		r.Header.Set("Origin", a.config.Origin)
		r.Header.Set("X-Auth-Request-Email", a.config.AllowedEmail)
		r.Header.Set("If-Match", b.state.ETag)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("%s %s: got %d: %s", request.method, request.path, w.Code, w.Body.String())
		}
	}
	after := map[string]*object{}
	for _, bucket := range []string{"private", "public", "protected"} {
		keys, err := store.List(t.Context(), bucket, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			after[bucket+"/"+key], err = store.Read(t.Context(), bucket, key)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("Rejected mutation changed catalog, publication marker or stored objects")
	}
}

func TestPrivateShareLinksStayPrivateAndRegenerateOnlyWhenPublished(t *testing.T) {
	store := &localStorage{root: t.TempDir()}
	b := newBrowser(t, store)
	for range 3 {
		b.raw("POST", "/api/photos", smallPNG(t), 200)
	}
	photos := append([]photo{}, b.state.Catalog.Photos...)
	for i, p := range photos {
		p.Alt = []string{"Link-only photograph", "Shared public photograph", "Unpublished draft"}[i]
		b.request("PATCH", "/api/photos/"+p.ID, edit(p), 200)
	}
	b.request("POST", "/api/albums", map[string]string{"title": "Public collection"}, 200)
	publicID := b.state.Catalog.Albums[0].ID
	b.request("PUT", "/api/albums/"+publicID, map[string]any{"title": "Public collection", "slug": "public", "published": true, "photoIds": []string{photos[1].ID}}, 200)
	b.request("POST", "/api/albums", map[string]string{"title": "Private link collection"}, 200)
	id := b.state.Catalog.Albums[1].ID
	form := map[string]any{"title": "Private link collection", "slug": "private-link", "description": "Private description", "access": "link", "published": true, "photoIds": []string{photos[0].ID, photos[1].ID}}
	response := b.request("PUT", "/api/albums/"+id, form, 200)
	if b.state.Catalog.Version != 3 || b.state.Catalog.Albums[1].PasswordSet {
		t.Fatal("Link access must reject older publishers without requiring a password")
	}
	var admin struct {
		Catalog struct {
			Albums []struct {
				LinkToken string `json:"linkToken"`
			} `json:"albums"`
		} `json:"catalog"`
	}
	json.Unmarshal(response, &admin)
	token := admin.Catalog.Albums[1].LinkToken
	if len(token) != 64 || bytes.Contains(response, []byte("linkHash")) {
		t.Fatal("Admin must receive the generated 256-bit link token, not its verifier")
	}
	var preview struct {
		Changes publicationChanges `json:"publicationChanges"`
	}
	json.Unmarshal(response, &preview)
	if preview.Changes.Photos.Next != 1 || preview.Changes.Albums.Next != 1 || preview.Changes.ProtectedPhotos.Next != 2 || preview.Changes.ProtectedAlbums.Next != 1 {
		t.Fatalf("Incorrect private-link/public audience counts: %+v", preview.Changes)
	}
	b.publish("succeeded")
	var live, protected catalog
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	value, err := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	if err != nil || value == nil {
		t.Fatalf("Missing private-link delivery release: %v", err)
	}
	json.Unmarshal(value.Body, &protected)
	if len(live.Photos) != 1 || live.Photos[0].ID != photos[1].ID || len(live.Albums) != 1 || live.Albums[0].ID != publicID || len(protected.Photos) != 2 || len(protected.Albums) != 1 {
		t.Fatal("Private-link albums or their shared photos went to the wrong audience")
	}
	item := protected.Albums[0]
	if item.Access != "link" || item.PasswordHash != "" || item.LinkToken != "" || item.LinkHash != fmt.Sprintf("%x", sha256.Sum256([]byte(token))) || len(item.AccessVersion) != 32 {
		t.Fatal("Link delivery needs only its SHA256 verifier and session version, never the bearer secret")
	}
	keys, _ := store.List(t.Context(), "public", "")
	for _, key := range keys {
		object, err := store.Read(t.Context(), "public", key)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{id, photos[0].ID, token, item.LinkHash, "Private link collection", "Private description", "Link-only photograph"} {
			if strings.Contains(key, secret) || bytes.Contains(object.Body, []byte(secret)) {
				t.Fatalf("Public object %s exposes private-link content or credentials", key)
			}
		}
	}
	b.request("PUT", "/api/albums/"+id, form, 200)
	if b.state.Catalog.Albums[1].LinkToken != token {
		t.Fatal("Ordinary save changed the share link")
	}
	form["regenerateLink"] = true
	b.request("PUT", "/api/albums/"+id, form, 200)
	newToken := b.state.Catalog.Albums[1].LinkToken
	if len(newToken) != 64 || newToken == token {
		t.Fatal("Regeneration did not create a new 256-bit token")
	}
	unchanged, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	if !bytes.Equal(value.Body, unchanged.Body) {
		t.Fatal("Draft link regeneration changed live access")
	}
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	rotated, _ := store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	protected = catalog{}
	json.Unmarshal(rotated.Body, &protected)
	if protected.Albums[0].LinkHash == item.LinkHash || protected.Albums[0].AccessVersion == item.AccessVersion || bytes.Contains(rotated.Body, []byte(newToken)) {
		t.Fatal("Publication must rotate the verifier and revoke old sessions without publishing the bearer token")
	}
	delete(form, "regenerateLink")
	b.request("GET", "/media/photos/"+photos[0].ID+"/640.jpg", nil, 404)
	form["access"] = "password"
	b.request("PUT", "/api/albums/"+id, form, 400)
	form["password"] = "new private album passphrase"
	b.request("PUT", "/api/albums/"+id, form, 200)
	if b.state.Catalog.Version != 3 || b.state.Catalog.Albums[1].LinkToken != "" {
		t.Fatal("Password access downgraded the catalog or retained a bearer token")
	}
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	value, _ = store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	json.Unmarshal(value.Body, &protected)
	if protected.Albums[0].Access != "password" || protected.Albums[0].PasswordHash == "" {
		t.Fatal("Published password access did not replace link access")
	}
	form["access"] = "link"
	b.request("PUT", "/api/albums/"+id, form, 400)
	delete(form, "password")
	b.request("PUT", "/api/albums/"+id, form, 200)
	if b.state.Catalog.Albums[1].PasswordSet {
		t.Fatal("Link access retained the old password")
	}
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	value, _ = store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	// Decode into a fresh value so omitted credential fields cannot retain old data.
	protected = catalog{}
	json.Unmarshal(value.Body, &protected)
	if protected.Albums[0].Access != "link" || protected.Albums[0].PasswordHash != "" || protected.Albums[0].LinkHash == "" || protected.Albums[0].AccessVersion == "" {
		t.Fatal("Returning to link access retained stale password credentials")
	}
	form["published"] = false
	b.request("PUT", "/api/albums/"+id, form, 200)
	b.publish("succeeded")
	json.Unmarshal(b.request("GET", "/media/index.json", nil, 200), &live)
	value, _ = store.Read(t.Context(), "protected", "releases/"+live.ProtectedRelease+".json")
	json.Unmarshal(value.Body, &protected)
	if len(protected.Albums) != 0 || len(protected.Photos) != 0 || len(live.Photos) != 1 {
		t.Fatal("Unpublishing link access changed public membership or kept private access")
	}
}
