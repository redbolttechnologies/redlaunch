package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectLockTryAcquireReportsBusyWithoutBlocking(t *testing.T) {
	manager := newProjectLockManager()
	held, err := manager.acquire(context.Background(), "application:7")
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()

	start := time.Now()
	if _, err := manager.tryAcquire("application:7"); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("tryAcquire contended = %v, want ErrProjectBusy", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tryAcquire blocked for %v, want immediate", elapsed)
	}
	if _, err := manager.tryAcquire("application:8"); err != nil {
		t.Fatalf("tryAcquire free = %v, want nil", err)
	}
}
