package handler

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	proxyActionJobStateRunning  = "running"
	proxyActionJobStateComplete = "complete"
	proxyActionJobStateFailed   = "failed"

	proxyActionJobRetention = time.Hour
)

// ErrProxyActionBusy reports that another proxy operation is already running.
// The proxy is a single shared component, so all start/stop/restart share one key.
var ErrProxyActionBusy = errors.New("another proxy operation is already running")

type proxyActionJobStore struct {
	mu   sync.Mutex
	jobs map[string]*proxyActionJob
}

type proxyActionJob struct {
	mu sync.RWMutex

	id         string
	operation  string
	state      string
	errorDetail string
	finishedAt time.Time
}

type proxyActionProgressData struct {
	JobID       string
	Operation   string
	State       string
	ErrorDetail string
	StatusURL   string
	CloseURL    string
}

func newProxyActionJobStore() *proxyActionJobStore {
	return &proxyActionJobStore{jobs: make(map[string]*proxyActionJob)}
}

func (s *proxyActionJobStore) createUnique(operation string) (*proxyActionJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &proxyActionJob{
		id:        id,
		operation: operation,
		state:     proxyActionJobStateRunning,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingOperation := existing.operation
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if state == proxyActionJobStateRunning {
			if existingOperation == operation {
				return existing, false, nil
			}
			return nil, false, ErrProxyActionBusy
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > proxyActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *proxyActionJobStore) get(id string) *proxyActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

func (s *proxyActionJobStore) active() []*proxyActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []*proxyActionJob
	for _, job := range s.jobs {
		job.mu.RLock()
		isRunning := job.state == proxyActionJobStateRunning
		job.mu.RUnlock()
		if isRunning {
			running = append(running, job)
		}
	}
	return running
}

func (s *proxyActionJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > proxyActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *proxyActionJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != proxyActionJobStateRunning {
		return
	}
	j.state = proxyActionJobStateComplete
	j.finishedAt = time.Now()
}

func (j *proxyActionJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != proxyActionJobStateRunning {
		return
	}
	j.state = proxyActionJobStateFailed
	j.errorDetail = proxyActionUserMessage(j.operation)
	j.finishedAt = time.Now()
}

func (j *proxyActionJob) snapshot() proxyActionProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return proxyActionProgressData{
		JobID:       j.id,
		Operation:   j.operation,
		State:       j.state,
		ErrorDetail: j.errorDetail,
	}
}

func (h *Handler) runProxyActionJob(ctx context.Context, job *proxyActionJob) {
	var err error
	switch job.operation {
	case "start":
		err = h.proxyActions.StartProxy(ctx)
	case "stop":
		err = h.proxyActions.StopProxy(ctx)
	case "restart":
		err = h.proxyActions.RestartProxy(ctx)
	default:
		err = errors.New("unknown proxy operation")
	}
	if err != nil {
		job.fail(err)
		h.logger.Error("run proxy action job", "operation", job.operation, "error", err)
		return
	}
	job.complete()
}

func proxyActionUserMessage(operation string) string {
	switch operation {
	case "start":
		return "The proxy could not be started."
	case "stop":
		return "The proxy could not be stopped."
	case "restart":
		return "The proxy could not be restarted."
	default:
		return "The proxy operation could not be completed."
	}
}
