package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestServiceActionJobDedupesSameOperationAndRejectsConflicting(t *testing.T) {
	store := newServiceActionJobStore()
	first, created, err := store.createUnique(7, "db", "start")
	if err != nil || !created {
		t.Fatalf("createUnique() = (%v, %v, %v), want created", first, created, err)
	}
	same, created, err := store.createUnique(7, "db", "start")
	if err != nil || created || same != first {
		t.Fatalf("duplicate start = (%v, %v, %v), want existing job", same, created, err)
	}
	if _, _, err := store.createUnique(7, "db", "stop"); !errors.Is(err, ErrServiceActionBusy) {
		t.Fatalf("conflicting stop error = %v, want ErrServiceActionBusy", err)
	}
	other, created, err := store.createUnique(7, "cache", "stop")
	if err != nil || !created || other == first {
		t.Fatalf("different service = (%v, %v, %v), want new job", other, created, err)
	}
}

func TestServiceActionJobTracksCompletionAndExpiry(t *testing.T) {
	store := newServiceActionJobStore()
	job, _, err := store.createUnique(7, "db", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(store.active(7)); got != 1 {
		t.Fatalf("active() = %d, want 1", got)
	}
	job.complete()
	if got := len(store.active(7)); got != 0 {
		t.Fatalf("active() after complete = %d, want 0", got)
	}
	snapshot := job.snapshot()
	if snapshot.State != serviceActionJobStateComplete {
		t.Fatalf("snapshot state = %q, want complete", snapshot.State)
	}
	job.fail(errors.New("late"))
	if snapshot := job.snapshot(); snapshot.State != serviceActionJobStateComplete {
		t.Fatalf("state after late fail = %q, want complete", snapshot.State)
	}

	failed, _, _ := store.createUnique(7, "db", "stop")
	failed.fail(errors.New("boom"))
	if snapshot := failed.snapshot(); snapshot.State != serviceActionJobStateFailed || snapshot.ErrorDetail == "" {
		t.Fatalf("failed snapshot = %+v, want failed with detail", snapshot)
	}
	failed.mu.Lock()
	failed.finishedAt = time.Now().Add(-2 * serviceActionJobRetention)
	failed.mu.Unlock()
	store.expire(time.Now())
	if got := store.get(7, "db", failed.id); got != nil {
		t.Fatalf("expired job still present")
	}
}

func TestServiceActionJobRetainsParametersAndCapturedResultUntilExpiry(t *testing.T) {
	store := newServiceActionJobStore()
	job, _, err := store.createUnique(7, "migrate", "run")
	if err != nil {
		t.Fatal(err)
	}
	parameters := application.OneOffRunParameters{
		Args: []string{"--confirm"},
		Env:  map[string]string{"NODE_ENV": "development"},
	}
	job.setParameters(parameters)
	parameters.Args[0] = "mutated"
	parameters.Env["NODE_ENV"] = "mutated"
	if got := job.takeParameters(); strings.Join(got.Args, "\x00") != "--confirm" || got.Env["NODE_ENV"] != "development" {
		t.Fatalf("job parameters = %#v, want defensive copy", got)
	}
	job.setRunResult(application.OneOffRunResult{ExitCode: 1, Stdout: "out", Stderr: "err", Truncated: true})
	job.fail(errors.New("run failed"))
	snapshot := job.snapshot()
	if snapshot.State != serviceActionJobStateFailed || snapshot.ExitCode != 1 || !snapshot.OutputAvailable || !snapshot.OutputTruncated {
		t.Fatalf("snapshot = %+v, want failed result metadata", snapshot)
	}
	result, ok := job.runResultSnapshot()
	if !ok || result.Stdout != "out" || result.Stderr != "err" {
		t.Fatalf("run result = %+v, %v, want captured output", result, ok)
	}
	job.mu.Lock()
	job.finishedAt = time.Now().Add(-2 * serviceActionJobRetention)
	job.mu.Unlock()
	store.expire(time.Now())
	if store.get(7, "migrate", job.id) != nil {
		t.Fatal("expired job still reachable from the store")
	}
}

func TestProxyActionJobSerializesSingleOperation(t *testing.T) {
	store := newProxyActionJobStore()
	first, created, err := store.createUnique("restart")
	if err != nil || !created {
		t.Fatalf("createUnique() = (%v, %v, %v), want created", first, created, err)
	}
	same, created, err := store.createUnique("restart")
	if err != nil || created || same != first {
		t.Fatalf("duplicate restart = (%v, %v, %v), want existing", same, created, err)
	}
	if _, _, err := store.createUnique("stop"); !errors.Is(err, ErrProxyActionBusy) {
		t.Fatalf("conflicting stop error = %v, want ErrProxyActionBusy", err)
	}
	if got := len(store.active()); got != 1 {
		t.Fatalf("active() = %d, want 1", got)
	}
	first.complete()
	if got := len(store.active()); got != 0 {
		t.Fatalf("active() after complete = %d, want 0", got)
	}
}
