package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	RequestID   string `json:"requestId,omitempty"`
}

// Encoding stays serial; only the already-encoded derivatives are transferred
// concurrently. Join every worker, even on failure, before allowing a catalog
// commit or retry/cancellation to touch these keys.
func (a *app) writeDerivatives(ctx context.Context, id string, variants []derivative, cache string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan derivative, len(variants))
	for _, variant := range variants {
		jobs <- variant
	}
	close(jobs)
	var workers sync.WaitGroup
	var failure error
	var once sync.Once
	for range min(3, len(variants)) {
		workers.Go(func() {
			for variant := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := a.store.Write(ctx, "private", fmt.Sprintf("photos/%s/%d.jpg", id, variant.Size), variant.Body, "image/jpeg", cache, nil); err != nil {
					once.Do(func() { failure = err; cancel() })
					return
				}
			}
		})
	}
	workers.Wait()
	if failure != nil {
		return failure
	}
	return ctx.Err()
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
	// Transfer creates new private objects, independent of catalog processing.
	job := upload{ID: identifier(), Filename: filepath.Base(input.Filename), Size: input.Size, Kind: kind, Expires: time.Now().Add(time.Hour).Unix(), RequestID: fmt.Sprintf("%d-%s", time.Now().UnixMilli(), identifier())}
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
	respond(w, 200, map[string]any{"upload": map[string]any{"id": job.ID, "requestId": job.RequestID, "filename": job.Filename, "size": job.Size}, "partSize": uploadPartSize, "parts": parts})
	return nil
}

func (a *app) localUploadPart(w http.ResponseWriter, r *http.Request) error {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
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
	if err := a.store.WriteFile(r.Context(), "private", fmt.Sprintf("multipart/%s/%d", job.MultipartID, number), file, "application/octet-stream", "no-store", nil); err != nil {
		return err
	}
	respond(w, 200, map[string]bool{"ok": true})
	return nil
}

// The browser transfers the entire selection before handing it to one worker.
// Reuse single-file completion so conversions remain serialized and each saved
// draft survives a later file's failure or the browser leaving the page.
func (a *app) processUploads(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	if len(input.IDs) == 0 {
		return badRequest("Choose at least one upload to process.")
	}
	seen := map[string]bool{}
	validID := regexp.MustCompile(`^[a-f0-9]{32}$`)
	for _, id := range input.IDs {
		if !validID.MatchString(id) || seen[id] {
			return badRequest("Upload IDs must be valid and unique.")
		}
		seen[id] = true
	}
	recordProgress := func(completed int) error {
		if r.Context().Value(operationContext{}) != true {
			return nil
		}
		job := a.operationStatus()
		job.Completed, job.Total = completed, len(input.IDs)
		if err := a.saveOperation(r.Context(), job); err != nil {
			return err
		}
		a.setOperation(job)
		return nil
	}
	if err := recordProgress(0); err != nil {
		return err
	}
	var failures []string
	for i, id := range input.IDs {
		if err := r.Context().Err(); err != nil {
			return err
		}
		request := r.Clone(r.Context())
		request.SetPathValue("id", id)
		result := &operationResponse{header: http.Header{}, status: 200}
		a.handler(a.completeUpload)(result, request)
		if result.status >= 400 {
			var failure struct{ Error string }
			json.Unmarshal(result.body.Bytes(), &failure)
			name := id
			if job, err := a.loadUpload(r.Context(), id); err == nil {
				name = job.Filename
			}
			failures = append(failures, name+": "+failure.Error)
			// Best effort, just like a cancelled browser upload. Cancellation
			// refuses committed items, including a save with failed cleanup.
			if err := a.cancelUpload(&operationResponse{header: http.Header{}, status: 200}, request); err != nil {
				log.Printf("Cannot clean up failed batch upload: %T", err)
			}
		}
		if err := recordProgress(i + 1); err != nil {
			return err
		}
	}
	if len(failures) != 0 {
		return badRequest(fmt.Sprintf("%d of %d uploads could not finish processing. Completed library items are kept. %s", len(failures), len(input.IDs), strings.Join(failures, "; ")))
	}
	return a.getCatalog(w, r)
}

func (a *app) completeUpload(w http.ResponseWriter, r *http.Request) error {
	if err := a.lockCatalog(); err != nil {
		return err
	}
	defer a.mu.Unlock()
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	if err := a.checkUploadUnowned(id); err != nil {
		return err
	}
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
	job, err := a.finalizeUpload(ctx, id)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "studio-process-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	p, files, err := a.prepareUpload(ctx, job, tmp)
	if err != nil {
		return err
	}
	if job.Kind == "video" {
		key := "photos/" + id + "/video.mp4"
		file, err := os.Open(files[key])
		if err != nil {
			return err
		}
		err = a.store.WriteFile(ctx, "private", key, file, mediaContentType(key), "private, no-store", nil)
		file.Close()
		if err != nil {
			return err
		}
	}
	var variants []derivative
	for _, size := range []int{640, 1280, 2560} {
		body, err := os.ReadFile(files[fmt.Sprintf("photos/%s/%d.jpg", id, size)])
		if err != nil {
			return err
		}
		variants = append(variants, derivative{Size: size, Body: body})
	}
	if err := a.writeDerivatives(ctx, id, variants, "private, no-store"); err != nil {
		return err
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

// The caller holds uploadMu against cancellation. SQLite does not own transfers.
func (a *app) finalizeUpload(ctx context.Context, id string) (upload, error) {
	job, err := a.loadUpload(ctx, id)
	if err != nil {
		return job, err
	}
	key := "originals/" + id
	size, err := a.store.Size(ctx, "private", key)
	var responseError *smithyhttp.ResponseError
	missing := errors.Is(err, os.ErrNotExist) || (errors.As(err, &responseError) && responseError.HTTPStatusCode() == 404)
	if err != nil && !missing {
		return job, err
	}
	// HEAD recovers an uncertain multipart completion, even after expiry.
	if missing {
		if job.Completed || time.Now().Unix() > job.Expires {
			return job, &requestError{410, "Upload expired or its original is missing. Start again."}
		}
		store := a.store.(multipartStorage)
		parts, err := store.ListUploadParts(ctx, key, job.MultipartID)
		if err != nil {
			return job, err
		}
		if int64(len(parts)) != (job.Size+uploadPartSize-1)/uploadPartSize {
			return job, badRequest("Some upload parts are missing. Retry the upload.")
		}
		for i, part := range parts {
			if part.Number != int32(i+1) || part.ETag == "" || part.Size != min(uploadPartSize, job.Size-int64(i)*uploadPartSize) {
				return job, badRequest("Upload parts have the wrong size or order.")
			}
		}
		if err := store.FinishUpload(ctx, key, job.MultipartID, parts); err != nil {
			return job, err
		}
		size, err = a.store.Size(ctx, "private", key)
		if err != nil {
			return job, err
		}
	}
	if size != job.Size {
		return job, badRequest("Completed upload has the wrong size.")
	}
	if !job.Completed {
		job.Completed = true
		body, err := encode(job)
		if err != nil {
			return job, err
		}
		if err := a.store.Write(ctx, "private", "uploads/"+id+".json", body, "application/json", "no-store", nil); err != nil {
			return job, err
		}
	}
	return job, nil
}

type uploadConversionError struct{ error }

func (e *uploadConversionError) Unwrap() error { return e.error }

// Conversion produces only local files; remote writes happen after preparation.
func (a *app) prepareUpload(ctx context.Context, job upload, tmp string) (photo, map[string]string, error) {
	p := photo{ID: job.ID, Filename: job.Filename, Kind: job.Kind, Tags: []string{}, Exif: map[string]string{}, UploadedAt: now()}
	files := map[string]string{}
	source, err := a.store.Open(ctx, "private", "originals/"+job.ID, 0, 0)
	if err != nil {
		return p, nil, err
	}
	defer source.Close()
	var variants []derivative
	if job.Kind == "video" {
		path := filepath.Join(tmp, "original")
		file, err := os.Create(path)
		if err != nil {
			return p, nil, err
		}
		n, copyErr := io.Copy(file, io.LimitReader(source, job.Size+1))
		closeErr := file.Close()
		if copyErr != nil {
			return p, nil, copyErr
		}
		if closeErr != nil {
			return p, nil, closeErr
		}
		if n != job.Size {
			return p, nil, badRequest("Stored upload size changed.")
		}
		result, err := processVideo(ctx, path, tmp)
		if err != nil {
			return p, nil, &uploadConversionError{err}
		}
		p.Width, p.Height, p.Duration, variants = result.Width, result.Height, result.Duration, result.Variants
		files["photos/"+job.ID+"/video.mp4"] = result.Path
	} else {
		body, err := io.ReadAll(io.LimitReader(source, (30<<20)+1))
		if err != nil {
			return p, nil, err
		}
		if int64(len(body)) != job.Size {
			return p, nil, badRequest("Stored upload size changed.")
		}
		p.Width, p.Height, p.Exif, p.CapturedDate, variants, err = processImage(body)
		if err != nil {
			return p, nil, &uploadConversionError{err}
		}
	}
	for _, variant := range variants {
		if variant.Size == 640 {
			p.BlurHash = previewBlurHash(variant.Body)
		}
		path := filepath.Join(tmp, fmt.Sprintf("%d.jpg", variant.Size))
		if err := os.WriteFile(path, variant.Body, 0600); err != nil {
			return p, nil, err
		}
		files[fmt.Sprintf("photos/%s/%d.jpg", job.ID, variant.Size)] = path
	}
	return p, files, nil
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
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	ctx, id := r.Context(), r.PathValue("id")
	if err := a.checkUploadUnowned(id); err != nil {
		return err
	}
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
