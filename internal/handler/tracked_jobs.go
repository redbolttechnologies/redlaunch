package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	// errTrackedJobCapacity is deliberately kept separate from duplicate
	// admission. Callers can report a retryable capacity response without
	// confusing it with an already-running operation.
	errTrackedJobCapacity  = errors.New("operation capacity is full")
	errTrackedJobDuplicate = errors.New("operation is already running")
	errTrackedJobClosed    = errors.New("operation runtime is shutting down")
)

const (
	defaultTrackedJobWorkers  = 8
	defaultTrackedJobTimeout  = 15 * time.Minute
	trackedJobCleanupInterval = time.Minute
)

// trackedJobRuntime owns the process-local execution lifecycle for all
// asynchronous HTTP operations. Feature-specific job stores retain only the
// progress data needed by their status pages; this type owns admission,
// cancellation, deadlines, and shutdown draining.
type trackedJobRuntime struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration

	slots           chan struct{}
	cleanupInterval time.Duration

	mu       sync.Mutex
	running  map[string]struct{}
	closed   bool
	workers  sync.WaitGroup
	cleanups []func(time.Time)
}

type trackedJobLease struct {
	runtime *trackedJobRuntime
	key     string

	once sync.Once
}

func newTrackedJobRuntime(parent context.Context, maxWorkers int, timeout time.Duration) *trackedJobRuntime {
	return newTrackedJobRuntimeWithInterval(parent, maxWorkers, timeout, trackedJobCleanupInterval)
}

func newTrackedJobRuntimeWithInterval(parent context.Context, maxWorkers int, timeout, cleanupInterval time.Duration) *trackedJobRuntime {
	if parent == nil {
		parent = context.Background()
	}
	if maxWorkers < 1 {
		maxWorkers = defaultTrackedJobWorkers
	}
	if timeout <= 0 {
		timeout = defaultTrackedJobTimeout
	}
	if cleanupInterval <= 0 {
		cleanupInterval = trackedJobCleanupInterval
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &trackedJobRuntime{
		ctx:             ctx,
		cancel:          cancel,
		timeout:         timeout,
		slots:           make(chan struct{}, maxWorkers),
		cleanupInterval: cleanupInterval,
		running:         make(map[string]struct{}),
	}
	go runtime.expiryLoop()
	return runtime
}

func (r *trackedJobRuntime) registerCleanup(cleanup func(time.Time)) {
	if cleanup == nil {
		return
	}
	r.mu.Lock()
	r.cleanups = append(r.cleanups, cleanup)
	r.mu.Unlock()
}

func (r *trackedJobRuntime) acquire(key string) (*trackedJobLease, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("operation key is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errTrackedJobClosed
	}
	if _, exists := r.running[key]; exists {
		return nil, errTrackedJobDuplicate
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return nil, errTrackedJobCapacity
	}
	r.running[key] = struct{}{}
	r.workers.Add(1)
	return &trackedJobLease{runtime: r, key: key}, nil
}

func (l *trackedJobLease) start(run func(context.Context)) {
	if run == nil {
		l.cancel()
		return
	}
	go func() {
		defer l.release()
		ctx, cancel := context.WithTimeout(l.runtime.ctx, l.runtime.timeout)
		defer cancel()
		run(ctx)
	}()
}

func (l *trackedJobLease) cancel() {
	l.release()
}

func (l *trackedJobLease) release() {
	l.once.Do(func() {
		r := l.runtime
		r.mu.Lock()
		delete(r.running, l.key)
		<-r.slots
		r.mu.Unlock()
		r.workers.Done()
	})
}

func (r *trackedJobRuntime) expiryLoop() {
	ticker := time.NewTicker(r.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.mu.Lock()
			cleanups := append([]func(time.Time){}, r.cleanups...)
			r.mu.Unlock()
			now := time.Now()
			for _, cleanup := range cleanups {
				cleanup(now)
			}
		case <-r.ctx.Done():
			return
		}
	}
}

// shutdown cancels every admitted operation and waits for all workers. A
// caller-provided deadline bounds how long shutdown itself can block when an
// external process ignores context cancellation.
func (r *trackedJobRuntime) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.cancel()
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) startTrackedJob(key string, run func(context.Context)) error {
	if h.jobs == nil {
		return errors.New("operation runtime is not configured")
	}
	lease, err := h.jobs.acquire(key)
	if err != nil {
		return err
	}
	lease.start(run)
	return nil
}
