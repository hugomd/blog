package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type upload struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Kind        string `json:"kind,omitempty"`
	MultipartID string `json:"multipartId"`
	Completed   bool   `json:"completed"`
	Expires     int64  `json:"expires"`
}

func (a *app) loadUpload(ctx context.Context, id string) (upload, error) {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return upload{}, &requestError{404, "Upload not found."}
	}
	value, err := a.store.Read(ctx, "private", "uploads/"+id+".json")
	if err != nil {
		return upload{}, err
	}
	if value == nil {
		return upload{}, &requestError{404, "Upload not found."}
	}
	var job upload
	if err := json.Unmarshal(value.Body, &job); err != nil {
		return upload{}, err
	}
	return job, nil
}

func (a *app) createUpload(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Filename, ContentType string
		Size                  int64
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	kind, limit := "", int64(30<<20)
	switch strings.ToLower(filepath.Ext(input.Filename)) {
	case ".mp4", ".mov":
		kind, limit = "video", 512<<20
	case ".jpg", ".jpeg", ".png":
	default:
		return badRequest("Choose a JPEG, PNG, MP4 or MOV file.")
	}
	if len(input.Filename) > 255 || input.Size < 1 || input.Size > limit {
		return badRequest("Images must be at most 30 MiB; videos at most 512 MiB.")
	}
	store, ok := a.store.(multipartStorage)
	if !ok {
		return &requestError{503, "Direct uploads are unavailable."}
	}
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	job := upload{ID: identifier(), Filename: filepath.Base(input.Filename), Size: input.Size, Kind: kind, Expires: time.Now().Add(time.Hour).Unix()}
	key := "originals/" + job.ID
	id, err := store.BeginUpload(r.Context(), key)
	if err != nil {
		return err
	}
	job.MultipartID = id
	accepted := false
	defer func() {
		if !accepted {
			ctx, cancel := context.WithTimeout(a.background, 30*time.Second)
			defer cancel()
			store.AbortUpload(ctx, key, id)
		}
	}()
	parts := []map[string]any{}
	for offset, number := int64(0), int32(1); offset < job.Size; offset, number = offset+uploadPartSize, number+1 {
		url, err := store.PartURL(r.Context(), key, id, number, min(uploadPartSize, job.Size-offset))
		if err != nil {
			return err
		}
		parts = append(parts, map[string]any{"number": number, "url": url})
	}
	body, err := encode(job)
	if err != nil {
		return err
	}
	if err := a.store.Write(r.Context(), "private", "uploads/"+job.ID+".json", body, "application/json", "no-store", nil); err != nil {
		return err
	}
	accepted = true
	respond(w, 200, map[string]any{"upload": map[string]any{"id": job.ID, "filename": job.Filename, "size": job.Size}, "partSize": uploadPartSize, "parts": parts})
	return nil
}

func (a *app) localUploadPart(w http.ResponseWriter, r *http.Request) error {
	job, err := a.loadUpload(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	number, err := strconv.ParseInt(r.PathValue("part"), 10, 32)
	if err != nil || number < 1 || number > (job.Size+uploadPartSize-1)/uploadPartSize {
		return badRequest("Invalid upload part.")
	}
	if job.Completed || time.Now().Unix() > job.Expires {
		return &requestError{410, "Upload is complete or expired. Start a new upload."}
	}
	size := min(uploadPartSize, job.Size-(number-1)*uploadPartSize)
	file, err := os.CreateTemp("", "studio-chunk-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	n, err := io.Copy(file, http.MaxBytesReader(w, r.Body, size))
	if err != nil || n != size {
		return badRequest("Upload part has the wrong size.")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := a.store.WriteFile(r.Context(), "private", fmt.Sprintf("multipart/%s/%d", job.MultipartID, number), file, "application/octet-stream", "no-store"); err != nil {
		return err
	}
	respond(w, 200, map[string]bool{"ok": true})
	return nil
}

func (a *app) completeUpload(w http.ResponseWriter, r *http.Request) error {
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	c, etag, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	for _, p := range c.Photos {
		if p.ID == id {
			if err := a.cleanupUpload(ctx, id); err != nil {
				return err
			}
			return a.respondCatalog(w, r, c, etag) // A lost completion response must not duplicate the item.
		}
	}
	job, err := a.loadUpload(ctx, id)
	if err != nil {
		return err
	}
	if time.Now().Unix() > job.Expires {
		return &requestError{410, "Upload expired. Cancel it and start again."}
	}
	store := a.store.(multipartStorage)
	key := "originals/" + id
	if !job.Completed {
		// HEAD also recovers a committed multipart completion whose response was lost.
		size, err := a.store.Size(ctx, "private", key)
		var responseError *smithyhttp.ResponseError
		missing := errors.Is(err, os.ErrNotExist) || (errors.As(err, &responseError) && responseError.HTTPStatusCode() == 404)
		if err != nil && !missing {
			return err
		}
		if missing {
			parts, err := store.ListUploadParts(ctx, key, job.MultipartID)
			if err != nil {
				return err
			}
			if int64(len(parts)) != (job.Size+uploadPartSize-1)/uploadPartSize {
				return badRequest("Some upload parts are missing. Retry the upload.")
			}
			for i, part := range parts {
				if part.Number != int32(i+1) || part.ETag == "" || part.Size != min(uploadPartSize, job.Size-int64(i)*uploadPartSize) {
					return badRequest("Upload parts have the wrong size or order.")
				}
			}
			if err := store.FinishUpload(ctx, key, job.MultipartID, parts); err != nil {
				return err
			}
			size, err = a.store.Size(ctx, "private", key)
			if err != nil {
				return err
			}
		}
		if size != job.Size {
			return badRequest("Completed upload has the wrong size.")
		}
		job.Completed = true
		body, err := encode(job)
		if err != nil {
			return err
		}
		if err := a.store.Write(ctx, "private", "uploads/"+id+".json", body, "application/json", "no-store", nil); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "studio-process-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	source, err := a.store.Open(ctx, "private", key, 0, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	p := photo{ID: id, Filename: job.Filename, Kind: job.Kind, Tags: []string{}, Exif: map[string]string{}, UploadedAt: now()}
	var variants []derivative
	if job.Kind == "video" {
		path := filepath.Join(tmp, "original")
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(file, io.LimitReader(source, job.Size+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != job.Size {
			return badRequest("Stored upload size changed.")
		}
		result, err := processVideo(ctx, path, tmp)
		if err != nil {
			return err
		}
		p.Width, p.Height, p.Duration, variants = result.Width, result.Height, result.Duration, result.Variants
		encoded, err := os.Open(result.Path)
		if err != nil {
			return err
		}
		defer encoded.Close()
		if err := a.store.WriteFile(ctx, "private", "photos/"+id+"/video.mp4", encoded, "video/mp4", "private, no-store"); err != nil {
			return err
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(source, (30<<20)+1))
		if err != nil {
			return err
		}
		if int64(len(body)) != job.Size {
			return badRequest("Stored upload size changed.")
		}
		p.Width, p.Height, p.Exif, p.CapturedDate, variants, err = processImage(body)
		if err != nil {
			return err
		}
	}
	for _, variant := range variants {
		if err := a.store.Write(ctx, "private", fmt.Sprintf("photos/%s/%d.jpg", id, variant.Size), variant.Body, "image/jpeg", "private, no-store", nil); err != nil {
			return err
		}
	}
	// Append to the latest locked catalog; never overwrite edits made during transfer.
	c.Photos = append(c.Photos, p)
	next, err := a.save(ctx, &c, etag)
	if err != nil {
		return err
	}
	if err := a.cleanupUpload(ctx, id); err != nil {
		return err
	}
	return a.respondCatalog(w, r, c, next)
}

// Retrying completion also finishes cleanup after an item was committed. Never
// remove originals or derivatives here: they now belong to the library.
func (a *app) cleanupUpload(ctx context.Context, id string) error {
	job, err := a.loadUpload(ctx, id)
	var requestErr *requestError
	if errors.As(err, &requestErr) && requestErr.status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if err := a.store.(multipartStorage).AbortUpload(ctx, "originals/"+id, job.MultipartID); err != nil {
		return err
	}
	return a.store.Delete(ctx, "private", "uploads/"+id+".json")
}

func (a *app) cancelUpload(w http.ResponseWriter, r *http.Request) error {
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	c, _, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	for _, p := range c.Photos {
		if p.ID == id {
			return &requestError{409, "This upload is already in the library. Use its normal delete action."}
		}
	}
	job, err := a.loadUpload(ctx, id)
	if err != nil {
		return err
	}
	if err := a.store.(multipartStorage).AbortUpload(ctx, "originals/"+id, job.MultipartID); err != nil {
		return err
	}
	for _, key := range append((photo{ID: id, Kind: job.Kind}).files(), "originals/"+id, "uploads/"+id+".json") {
		if err := a.store.Delete(ctx, "private", key); err != nil {
			return err
		}
	}
	respond(w, 200, map[string]bool{"ok": true})
	return nil
}
