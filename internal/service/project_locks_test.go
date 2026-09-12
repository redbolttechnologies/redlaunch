package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectLockManagerSerializesOneProjectWithoutBlockingAnother(t *testing.T) {
	manager := newProjectLockManager()
	first, err := manager.acquire(context.Background(), "application:1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()

	secondProject, err := manager.acquire(context.Background(), "application:2")
	if err != nil {
		t.Fatal(err)
	}
	secondProject.release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.acquire(ctx, "application:1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended project acquire error = %v, want deadline exceeded", err)
	}

	first.release()
	third, err := manager.acquire(context.Background(), "application:1")
	if err != nil {
		t.Fatal(err)
	}
	third.release()
}

func TestProjectLockManagerAdmissionIsCancellable(t *testing.T) {
	manager := newProjectLockManager()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.acquire(canceled, "free"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled uncontended acquire error = %v, want context canceled", err)
	}

	lease, err := manager.acquire(context.Background(), "proxy")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.acquire(ctx, "proxy"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled project acquire error = %v, want context canceled", err)
	}
	lease.release()

	if len(manager.locks) != 0 {
		t.Fatalf("project lock map = %#v, want canceled waiter and released owner removed", manager.locks)
	}
}
