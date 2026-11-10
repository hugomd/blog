package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

func albumPassword(password string) (string, error) {
	if count := utf8.RuneCountInString(password); count < 12 || count > 128 {
		return "", badRequest("Use an album password of 12–128 characters.")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	// OWASP's 16 MiB profile fits the delivery runtime without weakening the KDF.
	key, err := scrypt.Key([]byte(password), salt, 16384, 8, 5, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$16384$8$5$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

func (a *app) loadProtected(ctx context.Context, live catalog) (catalog, string, error) {
	if live.ProtectedRelease == "" {
		return catalog{Version: 1, Photos: []photo{}, Albums: []album{}}, "", nil
	}
	if !a.config.PrivateAlbums {
		return catalog{}, "", &requestError{503, "Protected album storage is not configured."}
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(live.ProtectedRelease) {
		return catalog{}, "", errors.New("invalid protected release pointer")
	}
	release, etag, err := a.load(ctx, "protected", "releases/"+live.ProtectedRelease+".json")
	if err == nil && etag == "" {
		err = errors.New("missing protected release")
	}
	return release, etag, err
}

// Archive redacts history; deletion also redacts protected snapshots so sessions
// cannot continue to discover deleted metadata through the current pointer.
func (a *app) redactPhoto(ctx context.Context, bucket, id string, history bool) error {
	keys := []string{"index.json"}
	if history {
		listed, err := a.store.List(ctx, bucket, "releases/")
		if err != nil {
			return err
		}
		keys = append(keys, listed...)
	}
	for _, key := range keys {
		if !strings.HasSuffix(key, ".json") {
			continue
		}
		live, etag, err := a.load(ctx, bucket, key)
		if err != nil {
			return err
		}
		if etag == "" {
			continue
		}
		if bucket == "public" && live.ProtectedRelease != "" && !a.config.PrivateAlbums {
			return &requestError{503, "Protected album storage is not configured."}
		}
		live.removePhoto(id)
		body, err := encode(live)
		if err != nil {
			return err
		}
		cache := "public, max-age=30, must-revalidate"
		if bucket == "protected" {
			cache = "private, no-store"
		}
		if err := a.store.Write(ctx, bucket, key, body, "application/json", cache, &etag); err != nil {
			return err
		}
	}
	return nil
}
