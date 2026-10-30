package handler

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/service"
)

const (
	registryMaintenanceJobRetention = "retention"
	registryMaintenanceJobGarbage   = "garbage-collect"

	registryMaintenanceJobStateRunning  = "running"
	registryMaintenanceJobStateComplete = "complete"
	registryMaintenanceJobStateFailed   = "failed"

	registryMaintenanceJobRetentionWindow = time.Hour
	registryMaintenanceJobTimeout         = 30 * time.Minute
)

type registryMaintenanceJobStore struct {
	mu   sync.Mutex
	jobs map[string]*registryMaintenanceJob
}

type registryMaintenanceJob struct {
	mu sync.RWMutex

	id         string
	operation  string
	state      string
	result     service.RegistryMaintenanceResult
	errorText  string
	finishedAt time.Time
}

type registryMaintenanceProgressData struct {
	JobID     string
	Operation string
	State     string
	Title     string
	Detail    string
	StatusURL string
	CloseURL  string
}

func newRegistryMaintenanceJobStore() *registryMaintenanceJobStore {
	return &registryMaintenanceJobStore{jobs: make(map[string]*registryMaintenanceJob)}
}

func (s *registryMaintenanceJobStore) createUnique(operation string) (*registryMaintenanceJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if state == registryMaintenanceJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > registryMaintenanceJobRetentionWindow {
			delete(s.jobs, jobID)
		}
	}
	job := &registryMaintenanceJob{id: id, operation: operation, state: registryMaintenanceJobStateRunning}
	s.jobs[id] = job
	return job, true, nil
}

func (s *registryMaintenanceJobStore) get(id string) *registryMaintenanceJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

func (s *registryMaintenanceJobStore) active() []*registryMaintenanceJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []*registryMaintenanceJob
	for _, job := range s.jobs {
		job.mu.RLock()
		isRunning := job.state == registryMaintenanceJobStateRunning
		job.mu.RUnlock()
		if isRunning {
			running = append(running, job)
		}
	}
	return running
}

func (s *registryMaintenanceJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > registryMaintenanceJobRetentionWindow {
			delete(s.jobs, id)
		}
	}
}

func (j *registryMaintenanceJob) complete(result service.RegistryMaintenanceResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != registryMaintenanceJobStateRunning {
		return
	}
	j.state = registryMaintenanceJobStateComplete
	j.result = result
	j.finishedAt = time.Now()
}

func (j *registryMaintenanceJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != registryMaintenanceJobStateRunning {
		return
	}
	j.state = registryMaintenanceJobStateFailed
	j.errorText = registryMaintenanceFailureText(j.operation, err)
	j.finishedAt = time.Now()
}

func (j *registryMaintenanceJob) snapshot() registryMaintenanceProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	data := registryMaintenanceProgressData{JobID: j.id, Operation: j.operation, State: j.state}
	switch j.state {
	case registryMaintenanceJobStateRunning:
		if j.operation == registryMaintenanceJobRetention {
			data.Title = "Purging older image versions…"
		} else {
			data.Title = "Collecting registry garbage…"
		}
		if j.operation == registryMaintenanceJobGarbage {
			data.Detail = "The registry is read-only while collection runs."
		}
	case registryMaintenanceJobStateComplete:
		if j.operation == registryMaintenanceJobRetention {
			data.Title = "Purge completed"
			switch j.result.DeletedVersions {
			case 0:
				data.Detail = "No older image versions needed removal."
			case 1:
				data.Detail = "Removed 1 older image version. Run garbage collection to reclaim disk space."
			default:
				data.Detail = "Removed " + strconv.Itoa(j.result.DeletedVersions) + " older image versions. Run garbage collection to reclaim disk space."
			}
		} else {
			data.Title = "Garbage collection completed"
			switch {
			case j.result.ReclaimedBytesKnown && j.result.ReclaimedBytes > 0:
				data.Detail = "Reclaimed " + dashboardSize(uint64(j.result.ReclaimedBytes)) + "."
			case j.result.ReclaimedBytesKnown:
				data.Detail = "No unused registry data needed removal."
			default:
				data.Detail = "Unused registry data was collected; reclaimed size is unavailable."
			}
		}
	case registryMaintenanceJobStateFailed:
		data.Title = "Registry maintenance failed"
		data.Detail = j.errorText
	}
	return data
}

func registryMaintenanceFailureText(operation string, err error) string {
	switch {
	case errors.Is(err, application.ErrRegistryBusy):
		return "Another registry maintenance operation is already running."
	case errors.Is(err, application.ErrRegistryUnavailable):
		return "The local registry is not available."
	}
	if operation == registryMaintenanceJobRetention {
		return "Older image versions could not be removed."
	}
	return "Registry garbage collection could not be completed."
}

func (h *Handler) startRegistryMaintenanceJob(ctx context.Context, job *registryMaintenanceJob) {
	runner, ok := h.applicationManager.(registryMaintenanceRunner)
	if !ok || runner == nil {
		job.fail(errors.New("registry maintenance is not configured"))
		return
	}
	var (
		result service.RegistryMaintenanceResult
		err    error
	)
	switch job.operation {
	case registryMaintenanceJobRetention:
		result, err = runner.RetainRegistryVersions(ctx)
	case registryMaintenanceJobGarbage:
		result, err = runner.CollectRegistryGarbage(ctx)
	default:
		err = errors.New("unknown registry maintenance operation")
	}
	if err != nil {
		job.fail(err)
		h.logger.Error("run registry maintenance job", "operation", job.operation, "error", err)
		return
	}
	job.complete(result)
}

func (h *Handler) registryMaintenanceProgress(jobID string) (*registryMaintenanceProgressData, bool) {
	if jobID == "" {
		return nil, false
	}
	job := h.registryMaintenanceJobs.get(jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/registry/maintenance/status?id=" + jobID
	progress.CloseURL = "/registry"
	return &progress, true
}

func (h *Handler) registryMaintenanceToastsForPage(explicitJobID string) []*registryMaintenanceProgressData {
	seen := make(map[string]struct{})
	var toasts []*registryMaintenanceProgressData
	if explicitJobID != "" {
		if job := h.registryMaintenanceJobs.get(explicitJobID); job != nil {
			snapshot := job.snapshot()
			snapshot.StatusURL = "/registry/maintenance/status?id=" + snapshot.JobID
			snapshot.CloseURL = "/registry"
			toasts = append(toasts, &snapshot)
			seen[snapshot.JobID] = struct{}{}
		}
	}
	for _, job := range h.registryMaintenanceJobs.active() {
		snapshot := job.snapshot()
		if _, ok := seen[snapshot.JobID]; ok {
			continue
		}
		snapshot.StatusURL = "/registry/maintenance/status?id=" + snapshot.JobID
		snapshot.CloseURL = "/registry"
		toasts = append(toasts, &snapshot)
	}
	return toasts
}

func (h *Handler) registryMaintenanceRunning() bool {
	return h.registryMaintenanceJobs != nil && len(h.registryMaintenanceJobs.active()) > 0
}
