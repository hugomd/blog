package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

type operation struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Error  string `json:"error,omitempty"`
}

type operationContext struct{}

func (a *app) operationStatus() operation {
	a.operationMu.RLock()
	defer a.operationMu.RUnlock()
	job := a.operation
	if job.State == "" {
		job.State = "idle"
	}
	return job
}

func (a *app) setOperation(job operation) {
	a.operationMu.Lock()
	a.operation = job
	a.operationMu.Unlock()
}

func (a *app) saveOperation(ctx context.Context, job operation) error {
	body, err := encode(job)
	if err != nil {
		return err
	}
	return a.store.Write(ctx, "private", "operation.json", body, "application/json", "no-store", nil)
}

// Do not replay an uncertain mutation after a restart: its catalog write or file
// cleanup may already have committed. Existing deletion/archive markers remain
// available for explicit retry after reloading the actual library state.
func (a *app) resumeOperation() error {
	value, err := a.store.Read(a.background, "private", "operation.json")
	if err != nil || value == nil {
		return err
	}
	var job operation
	if err := json.Unmarshal(value.Body, &job); err != nil {
		return err
	}
	if job.State == "running" {
		job.State, job.Error = "failed", "The server restarted during this change. Reload the library to check what completed before retrying."
		if err := a.saveOperation(a.background, job); err != nil {
			return err
		}
	}
	a.setOperation(job)
	return nil
}

// Existing synchronous clients retain their contract; Studio opts into a fast
// acknowledgement and polls the small status record instead of holding HTTP open.
func (a *app) change(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return a.handler(func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Prefer") != "respond-async" {
			return fn(w, r)
		}
		if err := a.lockCatalog(); err != nil {
			return err
		}
		defer a.mu.Unlock()
		if a.operationStatus().State == "running" {
			return &requestError{423, "Another change is in progress. Wait and retry."}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		job := operation{ID: identifier(), State: "running", Method: r.Method, Path: r.URL.Path}
		if err := a.saveOperation(r.Context(), job); err != nil {
			return err
		}
		a.setOperation(job)
		request := r.Clone(context.WithValue(a.background, operationContext{}, true))
		request.Body = io.NopCloser(bytes.NewReader(body))
		a.workers.Go(func() {
			// Wait until the accepting request releases the writer lock. Other
			// mutations are excluded by the running operation's status meanwhile.
			a.mu.Lock()
			a.mu.Unlock()
			ctx, cancel := context.WithTimeout(request.Context(), 30*time.Minute)
			defer cancel()
			result := &operationResponse{header: http.Header{}, status: 200}
			a.handler(fn)(result, request.WithContext(ctx))
			if a.background.Err() != nil {
				return
			}
			job.State = "succeeded"
			if result.status >= 400 {
				job.State = "failed"
				var failure struct{ Error string }
				json.Unmarshal(result.body.Bytes(), &failure)
				job.Error = failure.Error
			}
			if err := a.saveOperation(ctx, job); err != nil {
				log.Printf("Cannot save change outcome: %T", err)
				job.State, job.Error = "failed", "Could not record this change's outcome. Reload the library to check what completed before retrying."
			}
			a.setOperation(job)
		})
		w.Header().Set("Preference-Applied", "respond-async")
		w.Header().Set("Location", "/api/operation")
		respond(w, http.StatusAccepted, map[string]any{"operation": job})
		return nil
	})
}

// Successful handlers may render a full admin catalog; only retain error text.
type operationResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *operationResponse) Header() http.Header    { return w.header }
func (w *operationResponse) WriteHeader(status int) { w.status = status }
func (w *operationResponse) Write(p []byte) (int, error) {
	if w.status >= 400 {
		return w.body.Write(p)
	}
	return len(p), nil
}
