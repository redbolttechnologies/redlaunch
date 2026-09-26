package handler

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	serviceActionJobStateRunning  = "running"
	serviceActionJobStateComplete = "complete"
	serviceActionJobStateFailed   = "failed"

	serviceActionJobRetention = time.Hour
)

// ErrServiceActionBusy reports that another operation is already running for
// the same service. One key per service serializes start/stop/restart/run so
// concurrent Docker operations cannot interleave.
var ErrServiceActionBusy = errors.New("another operation is already running for this service")

type serviceActionJobStore struct {
	mu   sync.Mutex
	jobs map[string]*serviceActionJob
}

type serviceActionJob struct {
	mu sync.RWMutex

	id              string
	applicationID   int64
	serviceName     string
	operation       string
	state           string
	errorDetail     string
	errorTechnical  string
	finishedAt      time.Time
}

type serviceActionProgressData struct {
	JobID          string
	ApplicationID  int64
	ServiceName    string
	Operation      string
	State          string
	ErrorDetail    string
	ErrorTechnical string
	StatusURL      string
	CloseURL       string
}

func newServiceActionJobStore() *serviceActionJobStore {
	return &serviceActionJobStore{jobs: make(map[string]*serviceActionJob)}
}

func (s *serviceActionJobStore) createUnique(applicationID int64, serviceName, operation string) (*serviceActionJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &serviceActionJob{
		id:            id,
		applicationID: applicationID,
		serviceName:   serviceName,
		operation:     operation,
		state:         serviceActionJobStateRunning,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		existingServiceName := existing.serviceName
		existingOperation := existing.operation
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && existingServiceName == serviceName && state == serviceActionJobStateRunning {
			if existingOperation == operation {
				return existing, false, nil
			}
			return nil, false, ErrServiceActionBusy
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *serviceActionJobStore) get(applicationID int64, serviceName, id string) *serviceActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	job.mu.RLock()
	belongsToService := job.applicationID == applicationID && job.serviceName == serviceName
	job.mu.RUnlock()
	if !belongsToService {
		return nil
	}
	return job
}

func (s *serviceActionJobStore) getByID(applicationID int64, id string) *serviceActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	job.mu.RLock()
	belongsToApplication := job.applicationID == applicationID
	job.mu.RUnlock()
	if !belongsToApplication {
		return nil
	}
	return job
}

// active returns running jobs for one application so pages can render
// persistent non-blocking toasts even after navigation drops the ?job= param.
func (s *serviceActionJobStore) active(applicationID int64) []*serviceActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []*serviceActionJob
	for _, job := range s.jobs {
		job.mu.RLock()
		matches := job.applicationID == applicationID && job.state == serviceActionJobStateRunning
		job.mu.RUnlock()
		if matches {
			running = append(running, job)
		}
	}
	return running
}

func (s *serviceActionJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *serviceActionJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceActionJobStateRunning {
		return
	}
	j.state = serviceActionJobStateComplete
	j.finishedAt = time.Now()
}

func (j *serviceActionJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceActionJobStateRunning {
		return
	}
	j.state = serviceActionJobStateFailed
	// Preserve the same user-facing diagnostics as the former synchronous
	// error dialog (port-conflict hint + redacted Compose tail).
	dialog := newServiceActionError(j.operation, j.serviceName, err)
	j.errorDetail = dialog.Message
	j.errorTechnical = dialog.Detail
	j.finishedAt = time.Now()
}

func (j *serviceActionJob) snapshot() serviceActionProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return serviceActionProgressData{
		JobID:          j.id,
		ApplicationID:  j.applicationID,
		ServiceName:    j.serviceName,
		Operation:      j.operation,
		State:          j.state,
		ErrorDetail:    j.errorDetail,
		ErrorTechnical: j.errorTechnical,
	}
}

func (h *Handler) runServiceActionJob(ctx context.Context, job *serviceActionJob) {
	var err error
	switch job.operation {
	case "start":
		err = h.serviceActions.StartService(ctx, job.applicationID, job.serviceName)
	case "stop":
		err = h.serviceActions.StopService(ctx, job.applicationID, job.serviceName)
	case "restart":
		err = h.serviceActions.RestartService(ctx, job.applicationID, job.serviceName)
	case "run":
		err = h.serviceActions.RunServiceOnce(ctx, job.applicationID, job.serviceName)
	default:
		err = errors.New("unknown service operation")
	}
	if err != nil {
		job.fail(err)
		h.logger.Error("run service action job",
			"application_id", job.applicationID,
			"service", job.serviceName,
			"operation", job.operation,
			"error", serviceActionErrorDetail(err))
		return
	}
	job.complete()
}
