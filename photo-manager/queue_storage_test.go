package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSQLiteQueueTransactionsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	s, err := openQueueStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if other, err := openQueueStorage(path); err == nil {
		other.Close()
		t.Fatal("A second writer acquired the database")
	}
	first := queueJob{ID: "first", Action: "archive", State: "queued", Targets: []queueTarget{{ID: "photo-a", Token: "a"}}}
	if err := s.Commit(t.Context(), queueCommit{Generation: "one", Job: &first}); err != nil {
		t.Fatal(err)
	}
	// The job insert and generation update precede the conflicting reservation;
	// all three must roll back together, not leave a partially accepted job.
	second := queueJob{ID: "second", Action: "archive", State: "queued", Targets: []queueTarget{{ID: "photo-b", Token: "b"}, {ID: "photo-a", Token: "a"}}}
	if err := s.Commit(t.Context(), queueCommit{Expected: "one", Generation: "two", Job: &second}); err == nil {
		t.Fatal("Conflicting reservation committed")
	}
	q, err := s.Load(t.Context())
	if err != nil || q.Generation != "one" || len(q.Jobs) != 1 {
		t.Fatalf("Partial transaction: %+v %v", q, err)
	}
	second.Targets = second.Targets[:1]
	if err := s.Commit(t.Context(), queueCommit{Expected: "one", Generation: "two", Job: &second}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(t.Context(), queueCommit{Expected: "one", Generation: "stale", Job: &first}); !errors.Is(err, errConflict) {
		t.Fatalf("Stale transaction: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Commit(ctx, queueCommit{Expected: "two", Generation: "cancelled"}); err == nil {
		t.Fatal("Cancelled transaction committed")
	}
	first.State = "cancelled"
	if err := s.Commit(t.Context(), queueCommit{Expected: "two", Generation: "three", Job: &first}); err != nil {
		t.Fatal(err)
	}
	// Only terminal records may be pruned, even if a caller includes a pending ID.
	if err := s.Commit(t.Context(), queueCommit{Expected: "three", Generation: "four", Remove: []string{first.ID, second.ID}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = openQueueStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Load(t.Context())
	if err != nil || q.Generation != "four" || len(q.Jobs) != 1 || q.Jobs[0].ID != "second" {
		t.Fatalf("Wrong recovered jobs: %+v %v", q, err)
	}
	third := queueJob{ID: "third", Action: "restore", State: "queued", Targets: first.Targets}
	if err := s.Commit(t.Context(), queueCommit{Expected: "four", Generation: "five", Job: &third}); err != nil {
		t.Fatalf("Cancellation did not release reservations: %v", err)
	}
	q, _ = s.Load(t.Context())
	if q.Jobs[0].ID != "second" || q.Jobs[1].ID != "third" {
		t.Fatal("FIFO order changed across reopen/pruning")
	}
}

func TestSQLiteQueueSurvivesUncleanExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	child := exec.Command(os.Args[0], "-test.run=^TestSQLiteQueueCrashHelper$")
	child.Env = append(os.Environ(), "STUDIO_TEST_CRASH_DATABASE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("Crash writer: %v %s", err, output)
	}
	s, err := openQueueStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q, err := s.Load(t.Context())
	if err != nil || len(q.Jobs) != 1 || q.Jobs[0].ID != "accepted" || q.Jobs[0].Cleanup.After != "releases/024.json" {
		t.Fatalf("Lost durable progress: %+v %v", q, err)
	}
	var sync int
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("Queue is not using FULL durability: %d %v", sync, err)
	}
}

func TestSQLiteQueueCrashHelper(t *testing.T) {
	path := os.Getenv("STUDIO_TEST_CRASH_DATABASE")
	if path == "" {
		t.Skip("subprocess only")
	}
	s, err := openQueueStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	job := queueJob{ID: "accepted", Action: "archive", State: "running", Targets: []queueTarget{{ID: "photo", Token: "token"}}, Cleanup: cleanupProgress{IndexDone: true, After: "releases/024.json"}}
	if err := s.Commit(t.Context(), queueCommit{Generation: "committed", Job: &job}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0) // Deliberately skip Close, deferred cleanup and final WAL checkpoint.
}
