package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const queueLimit = 100
const queueHistoryLimit = 1000
const queueRetention = 24 * time.Hour

// Commands contain IDs, preconditions and processing metadata, never media bytes
// or credentials.
// Request IDs include their creation time so expired deduplication records cannot
// turn a delayed HTTP retry into a new action after history is pruned.
type queueTarget struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type queueJob struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Targets   []queueTarget   `json:"targets"`
	State     string          `json:"state"`
	CreatedAt string          `json:"createdAt"`
	Attempts  int             `json:"attempts"`
	Error     string          `json:"error,omitempty"`
	CanCancel bool            `json:"canCancel"`
	Cleanup   cleanupProgress `json:"cleanup"`
	Protected bool            `json:"protected"`
	Upload    *queuedUpload   `json:"upload,omitempty"`
}

type cleanupProgress struct {
	Bucket    int    `json:"bucket"`
	IndexDone bool   `json:"indexDone"`
	After     string `json:"after"`
}

type durableQueue struct {
	Version          int               `json:"version"`
	Generation       string            `json:"generation"`
	Jobs             []queueJob        `json:"jobs"`
	PublicationWrite *publicationWrite `json:"publicationWrite,omitempty"`
}

type queueView struct {
	Jobs  []queueJob `json:"jobs"`
	Error string     `json:"error,omitempty"`
}

func queuePending(job queueJob) bool {
	return job.State != "succeeded" && job.State != "cancelled" && job.State != "rejected"
}

type queueRejected struct{ error }

func (e *queueRejected) Unwrap() error { return e.error }

func photoToken(p photo) string {
	body, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

// All queue mutations hold queueMu. Never wait for mu while holding queueMu:
// the running catalog writer needs queueMu to record its result.
func (a *app) queueBlockedLocked() bool {
	return a.queueError != "" || slices.ContainsFunc(a.queue.Jobs, queuePending)
}

func (a *app) queueStatus() queueView {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	return queueView{Jobs: append([]queueJob{}, a.queue.Jobs...), Error: a.queueError}
}

// Commit one job and any expired records atomically with the next generation.
// Retry the exact transaction; never adopt a new generation after an error.
func (a *app) saveQueueLocked(ctx context.Context, next durableQueue) error {
	next.Version, next.Generation = 1, identifier()
	commit := queueCommit{Expected: a.queue.Generation, Generation: next.Generation, PublicationWrite: next.PublicationWrite}
	previous := map[string]queueJob{}
	for _, job := range a.queue.Jobs {
		previous[job.ID] = job
	}
	for _, job := range next.Jobs {
		old, found := previous[job.ID]
		if !found || !reflect.DeepEqual(old, job) {
			if commit.Job != nil {
				return errors.New("queue commit changes more than one job")
			}
			copy := job
			commit.Job = &copy
		}
		delete(previous, job.ID)
	}
	for id := range previous {
		commit.Remove = append(commit.Remove, id)
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := a.queueStore.Commit(ctx, commit)
		if err == nil {
			a.queue, a.queueError = next, ""
			return nil
		}
		value, readErr := a.queueStore.Load(ctx)
		if readErr == nil && value.Generation == next.Generation {
			a.queue, a.queueError = next, ""
			return nil
		}
		if ctx.Err() != nil || errors.Is(err, errConflict) {
			break
		}
	}
	a.queueError = "Queue storage outcome is uncertain. Recover the queue before making more changes."
	return &requestError{503, a.queueError}
}

func (a *app) loadQueueLocked(ctx context.Context) error {
	next, err := a.queueStore.Load(ctx)
	if err != nil {
		return err
	}
	if next.Version != 1 {
		return errors.New("unsupported queue version")
	}
	for _, job := range next.Jobs {
		if job.Action != "archive" && job.Action != "restore" && job.Action != "upload" {
			return errors.New("unsupported queued action")
		}
		if job.Action == "upload" && job.Upload == nil {
			return errors.New("missing queued upload")
		}
		switch job.State {
		case "queued", "running", "failed", "succeeded", "cancelled", "rejected":
		default:
			return errors.New("unsupported queue state")
		}
	}
	a.queue = next
	// Fence earlier in-flight queue writes, even after a negative read. Only
	// after this CAS succeeds may a new process submit or execute more work.
	return a.saveQueueLocked(ctx, next)
}

func (a *app) resumeQueue() error {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if err := a.loadQueueLocked(a.background); err != nil {
		return err
	}
	a.startQueueLocked()
	return nil
}

var queueIDPattern = regexp.MustCompile(`^[0-9]{13}-[a-f0-9]{32}$`)

func queueRequestTime(id string) (time.Time, error) {
	if !queueIDPattern.MatchString(id) {
		return time.Time{}, badRequest("Use a timestamp and random request ID for this action.")
	}
	millis, _ := strconv.ParseInt(strings.SplitN(id, "-", 2)[0], 10, 64)
	return time.UnixMilli(millis), nil
}

func (a *app) enqueue(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		ID      string        `json:"id"`
		Action  string        `json:"action"`
		Targets []queueTarget `json:"targets"`
	}
	if err := decodeRequest(w, r, &input); err != nil {
		return err
	}
	created, err := queueRequestTime(input.ID)
	if err != nil {
		return err
	}
	if (input.Action != "archive" && input.Action != "restore") || len(input.Targets) == 0 || len(input.Targets) > 1000 {
		return badRequest("Choose archive or restore and between 1 and 1000 files.")
	}
	slices.SortFunc(input.Targets, func(a, b queueTarget) int { return strings.Compare(a.ID, b.ID) })
	for i, target := range input.Targets {
		if target.ID == "" || target.Token == "" || (i > 0 && input.Targets[i-1].ID == target.ID) {
			return badRequest("Select each file once, with its current version.")
		}
	}
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueError != "" {
		return &requestError{503, a.queueError}
	}
	for _, job := range a.queue.Jobs {
		if job.ID == input.ID {
			if job.Action != input.Action || !slices.Equal(job.Targets, input.Targets) {
				return &requestError{409, "This request ID already belongs to a different action."}
			}
			respond(w, 202, map[string]any{"job": job})
			return nil
		}
	}
	if time.Since(created) > queueRetention || time.Until(created) > 5*time.Minute {
		return &requestError{409, "This request ID has expired or its clock is incorrect. Reload and submit a new action."}
	}
	if a.operationStatus().State == "running" || a.publicationStatus().State == "running" {
		return &requestError{423, "Wait for the current upload, edit or publication before queuing changes."}
	}
	// If there is no queued owner, exclude a synchronous writer that passed
	// the handler guard just before this submission.
	if !a.queueBlockedLocked() {
		if !a.mu.TryLock() {
			return &requestError{423, "Another change is in progress. Wait and retry."}
		}
		defer a.mu.Unlock()
	}
	selected := map[string]bool{}
	for _, target := range input.Targets {
		selected[target.ID] = true
	}
	next := a.queue
	next.Jobs = []queueJob{}
	pending := 0
	for _, job := range a.queue.Jobs {
		if queuePending(job) {
			pending++
			for _, target := range job.Targets {
				if selected[target.ID] {
					return &requestError{409, "A selected file already has a pending action. Wait or resolve it first."}
				}
			}
		}
		at, _ := queueRequestTime(job.ID)
		if queuePending(job) || time.Since(at) <= queueRetention {
			next.Jobs = append(next.Jobs, job)
		}
	}
	if pending >= queueLimit || len(next.Jobs) >= queueHistoryLimit {
		return &requestError{429, "The queue is full. Accepted jobs are kept; try again after jobs finish or history expires."}
	}
	c, _, err := a.load(r.Context(), "private", "catalog.json")
	if err != nil {
		return err
	}
	job := queueJob{ID: input.ID, Action: input.Action, Targets: input.Targets, State: "queued", CreatedAt: now(), CanCancel: true, Protected: a.config.PrivateAlbums}
	if err := validateQueueTargets(c, job); err != nil {
		return err
	}
	next.Jobs = append(next.Jobs, job)
	if err := a.saveQueueLocked(r.Context(), next); err != nil {
		return err
	}
	w.Header().Set("Location", "/api/queue")
	respond(w, 202, map[string]any{"job": job})
	a.startQueueLocked()
	return nil
}

func validateQueueTargets(c catalog, job queueJob) error {
	photos := map[string]photo{}
	for _, p := range c.Photos {
		photos[p.ID] = p
	}
	for _, target := range job.Targets {
		p, found := photos[target.ID]
		if !found || photoToken(p) != target.Token {
			return &requestError{409, "A selected file changed. Cancel this waiting action and reload before choosing again."}
		}
		if p.Deleting || (job.Action == "restore" && (!p.Archived || p.Archiving)) || (job.Action == "archive" && p.Archived && !p.Archiving) {
			return &requestError{409, "A selected file cannot be archived or restored in its current state."}
		}
	}
	return nil
}

func (a *app) updateQueueJobLocked(ctx context.Context, job queueJob) error {
	next := a.queue
	next.Jobs = slices.Clone(next.Jobs)
	for i := range next.Jobs {
		if next.Jobs[i].ID == job.ID {
			next.Jobs[i] = job
			return a.saveQueueLocked(ctx, next)
		}
	}
	return errors.New("queued job missing")
}

func (a *app) controlQueue(w http.ResponseWriter, r *http.Request) error {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueError != "" {
		return &requestError{503, a.queueError}
	}
	for _, job := range a.queue.Jobs {
		if job.ID != r.PathValue("id") {
			continue
		}
		switch r.PathValue("action") {
		case "cancel":
			if job.State == "cancelled" {
				break
			}
			if !job.CanCancel || (job.State != "queued" && job.State != "failed") {
				return &requestError{409, "Only work that has not started can be cancelled."}
			}
			job.State, job.Error, job.CanCancel = "cancelled", "", false
		case "retry":
			if job.State != "failed" && job.State != "rejected" {
				return &requestError{409, "Only a failed action can be retried."}
			}
			if job.State == "rejected" {
				pending := 0
				for _, other := range a.queue.Jobs {
					if queuePending(other) {
						pending++
					}
				}
				if pending >= queueLimit {
					return &requestError{429, "The queue is full. Retry after jobs finish."}
				}
			}
			job.State, job.Error = "queued", ""
		default:
			return &requestError{404, "Unknown queue action."}
		}
		if err := a.updateQueueJobLocked(r.Context(), job); err != nil {
			return err
		}
		respond(w, 202, map[string]any{"job": job})
		a.startQueueLocked()
		return nil
	}
	return &requestError{404, "Queued action not found."}
}

func (a *app) recoverQueue(w http.ResponseWriter, r *http.Request) error {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.queueWorker {
		return &requestError{423, "The worker is still finishing. Retry recovery shortly."}
	}
	if err := a.loadQueueLocked(r.Context()); err != nil {
		a.queueError = "Could not recover queue storage. Retry recovery before making changes."
		return err
	}
	respond(w, 202, map[string]bool{"recovered": true})
	a.startQueueLocked()
	return nil
}

func (a *app) startQueueLocked() {
	index := slices.IndexFunc(a.queue.Jobs, queuePending)
	if a.queueWorker || a.queueError != "" || a.background.Err() != nil || index < 0 || a.queue.Jobs[index].State == "failed" {
		return
	}
	a.queueWorker = true
	a.workers.Go(a.runQueue)
}

func (a *app) runQueue() {
	defer func() {
		a.queueMu.Lock()
		a.queueWorker = false
		// A submission can arrive after the loop decides to stop but before
		// this defer takes the lock. Do not lose that wakeup.
		a.startQueueLocked()
		a.queueMu.Unlock()
	}()
	for {
		a.queueMu.Lock()
		index := slices.IndexFunc(a.queue.Jobs, queuePending)
		if a.queueError != "" || index < 0 || a.queue.Jobs[index].State == "failed" || a.background.Err() != nil {
			a.queueMu.Unlock()
			return
		}
		job := a.queue.Jobs[index]
		job.State, job.CanCancel = "running", false
		if err := a.updateQueueJobLocked(a.background, job); err != nil {
			a.queueMu.Unlock()
			return
		}
		a.queueMu.Unlock()
		a.mu.Lock()
		ctx := a.background
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			job.Attempts++
			err = a.executeQueued(ctx, &job)
			var invalid *requestError
			if err == nil || errors.As(err, &invalid) || errors.Is(err, errConflict) || ctx.Err() != nil {
				break
			}
			if attempt < 2 {
				select {
				case <-ctx.Done():
				case <-time.After(time.Duration(attempt+1) * time.Second):
				}
			}
		}
		a.mu.Unlock()
		if a.background.Err() != nil {
			return // The durable running record is recovered on startup.
		}
		job.State, job.Error = "succeeded", ""
		if err != nil {
			job.State, job.Error = "failed", "Storage or processing could not finish. The queue is paused; retry this action to recover."
			var invalid *queueRejected
			if errors.As(err, &invalid) {
				job.Error, job.CanCancel = invalid.Error(), true // Rejected before writes.
				if job.Action == "upload" {
					job.State, job.CanCancel = "rejected", false
				}
			}
		}
		a.queueMu.Lock()
		err = a.updateQueueJobLocked(a.background, job)
		a.queueMu.Unlock()
		if err != nil {
			return
		}
	}
}

// A receipt lives in the SAME conditional catalog write as the effect. No
// later queued job or legacy writer runs before this job's outcome is durable.
// Archive's applying receipt owns its retryable markers until redaction ends.
func (a *app) executeQueued(ctx context.Context, job *queueJob) error {
	// Reconcile every possible late public/protected manifest write before
	// scanning history. SQLite cannot make R2 effects transactional by itself.
	if err := a.finishPublicationWrite(ctx); err != nil {
		return err
	}
	if job.Action == "upload" {
		return a.executeQueuedUpload(ctx, job)
	}
	c, etag, err := a.load(ctx, "private", "catalog.json")
	if err != nil {
		return err
	}
	if c.QueueReceipt == job.ID+":done" {
		return nil
	}
	resuming := c.QueueReceipt == job.ID+":applying"
	if !resuming {
		if err := validateQueueTargets(c, *job); err != nil {
			return &queueRejected{err}
		}
	}
	ids := make([]string, 0, len(job.Targets))
	selected := map[string]bool{}
	for _, target := range job.Targets {
		ids = append(ids, target.ID)
		selected[target.ID] = true
	}
	c.Version = 5 // Older binaries must not discard receipts or bypass the queue.
	if job.Action == "archive" {
		if !resuming {
			for i := range c.Photos {
				if p := &c.Photos[i]; selected[p.ID] {
					p.Archived, p.Archiving, p.Published = true, true, false
				}
			}
			c.QueueReceipt = job.ID + ":applying"
			etag, err = a.save(ctx, &c, etag)
			if err != nil {
				return err
			}
		}
		if err := a.cleanupQueued(ctx, job, ids); err != nil {
			return err
		}
	}
	for i := range c.Photos {
		if p := &c.Photos[i]; selected[p.ID] {
			p.Archived, p.Archiving, p.Published = job.Action == "archive", false, false
		}
	}
	c.QueueReceipt = job.ID + ":done"
	_, err = a.save(ctx, &c, etag)
	return err
}

// Bound work between checkpoints, not the lifetime of an unbounded archive.
// A restart repeats at most one page. Publication and all other manifest writers
// stay excluded until completion, so lexical StartAfter is a stable cursor.
func (a *app) cleanupQueued(ctx context.Context, job *queueJob, ids []string) error {
	if job.Protected && !a.config.PrivateAlbums {
		return &requestError{503, "Restore protected storage before resuming this archive."}
	}
	buckets := []string{"public"}
	if a.config.PrivateAlbums {
		buckets = append(buckets, "protected")
	}
	for job.Cleanup.Bucket < len(buckets) {
		bucket := buckets[job.Cleanup.Bucket]
		next := *job
		if !next.Cleanup.IndexDone {
			if err := a.redactManifest(ctx, bucket, "index.json", ids); err != nil {
				return err
			}
			next.Cleanup.IndexDone = true
		} else {
			pageCtx, cancel := context.WithTimeout(ctx, time.Minute)
			keys, err := a.store.ListPage(pageCtx, bucket, "releases/", next.Cleanup.After, 25)
			cancel()
			if err != nil {
				return err
			}
			for _, key := range keys {
				if strings.HasSuffix(key, ".json") {
					if err := a.redactManifest(ctx, bucket, key, ids); err != nil {
						return err
					}
				}
				next.Cleanup.After = key
			}
			if len(keys) == 0 {
				next.Cleanup = cleanupProgress{Bucket: next.Cleanup.Bucket + 1}
			}
		}
		a.queueMu.Lock()
		err := a.updateQueueJobLocked(ctx, next)
		a.queueMu.Unlock()
		if err != nil {
			return err
		}
		*job = next
	}
	return nil
}
