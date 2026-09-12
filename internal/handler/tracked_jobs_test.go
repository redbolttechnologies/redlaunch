package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrackedJobRuntimeSuppressesDuplicatesAndBoundsAdmission(t *testing.T) {
	runtime := newTrackedJobRuntimeWithInterval(context.Background(), 1, time.Second, time.Hour)
	first, err := runtime.acquire("application:7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.acquire("application:7"); !errors.Is(err, errTrackedJobDuplicate) {
		t.Fatalf("duplicate admission error = %v, want %v", err, errTrackedJobDuplicate)
	}
	if _, err := runtime.acquire("application:8"); !errors.Is(err, errTrackedJobCapacity) {
		t.Fatalf("capacity admission error = %v, want %v", err, errTrackedJobCapacity)
	}
	first.cancel()
	if err := runtime.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedJobRuntimeAppliesDeadlineAndDrainsOnShutdown(t *testing.T) {
	runtime := newTrackedJobRuntime(context.Background(), 2, 25*time.Millisecond)
	lease, err := runtime.acquire("hanging")
	if err != nil {
		t.Fatal(err)
	}
	deadlineObserved := make(chan struct{})
	lease.start(func(ctx context.Context) {
		<-ctx.Done()
		close(deadlineObserved)
	})
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("tracked job did not receive its execution deadline")
	}

	var drained atomic.Bool
	second, err := runtime.acquire("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	second.start(func(ctx context.Context) {
		<-ctx.Done()
		drained.Store(true)
	})
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if !drained.Load() {
		t.Fatal("shutdown returned before the tracked worker drained")
	}
}

func TestTrackedJobRuntimeExpiresRegisteredJobs(t *testing.T) {
	runtime := newTrackedJobRuntimeWithInterval(context.Background(), 1, time.Second, time.Millisecond)
	defer runtime.shutdown(context.Background())
	called := make(chan struct{}, 1)
	runtime.registerCleanup(func(time.Time) { called <- struct{}{} })
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("registered expiry cleanup was not called")
	}
}
