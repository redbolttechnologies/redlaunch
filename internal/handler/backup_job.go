package handler

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	backupJobOperationRun     = "run"
	backupJobOperationRestore = "restore"

	backupJobStateRunning  = "running"
	backupJobStateComplete = "complete"
	backupJobStateFailed   = "failed"

	backupJobRetention = time.Hour
)

type backupJobStore struct {
	mu   sync.Mutex
	jobs map[string]*backupJob
}

type backupJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	serviceName   string
	operation     string
	restoreFile   string
	state         string
	errorDetail   string
	operationErr  error
	finishedAt    time.Time
	done          chan struct{}
	doneOnce      sync.Once
}

type backupProgressData struct {
	JobID         string
	ApplicationID int64
	ServiceName   string
	Operation     string
	State         string
	ErrorDetail   string
	StatusURL     string
	CloseURL      string
}

func newBackupJobStore() *backupJobStore {
	return &backupJobStore{jobs: make(map[string]*backupJob)}
}

func (s *backupJobStore) createUnique(applicationID int64, serviceName, operation string) (*backupJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &backupJob{
		id:            id,
		applicationID: applicationID,
		serviceName:   serviceName,
		operation:     operation,
		state:         backupJobStateRunning,
		done:          make(chan struct{}),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		existingServiceName := existing.serviceName
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && existingServiceName == serviceName && state == backupJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > backupJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *backupJobStore) get(applicationID int64, serviceName, id string) *backupJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	job.mu.RLock()
	belongsToService := job.applicationID == applicationID && job.serviceName == serviceName
	finishedAt := job.finishedAt
	job.mu.RUnlock()
	if !belongsToService {
		return nil
	}
	if !finishedAt.IsZero() && time.Since(finishedAt) > backupJobRetention {
		delete(s.jobs, id)
		return nil
	}
	return job
}

func (s *backupJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > backupJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *backupJob) complete() {
	j.mu.Lock()
	if j.state == backupJobStateRunning {
		j.state = backupJobStateComplete
		j.finishedAt = time.Now()
	}
	j.mu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
}

func (j *backupJob) fail(err error) {
	j.mu.Lock()
	if j.state == backupJobStateRunning {
		j.state = backupJobStateFailed
		j.operationErr = err
		j.errorDetail = backupJobUserMessage(err)
		j.finishedAt = time.Now()
	}
	j.mu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
}

func (j *backupJob) wait(timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = 1 * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-j.done:
		j.mu.RLock()
		err := j.operationErr
		j.mu.RUnlock()
		return true, err
	case <-timer.C:
		return false, nil
	}
}

func (j *backupJob) snapshot() backupProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return backupProgressData{
		JobID:         j.id,
		ApplicationID: j.applicationID,
		ServiceName:   j.serviceName,
		Operation:     j.operation,
		State:         j.state,
		ErrorDetail:   j.errorDetail,
	}
}

func (j *backupJob) setRestoreFile(fileName string) {
	j.mu.Lock()
	j.restoreFile = fileName
	j.mu.Unlock()
}

func backupJobUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrBackupServiceNotRunning):
		return "The database service must be running before a backup can be created."
	case errors.Is(err, application.ErrBackupNotFound):
		return "The selected backup could not be found. Refresh the page and try again."
	case errors.Is(err, application.ErrBackupOperationInProgress):
		return "Another operation is already using this database service. Try again shortly."
	case errors.Is(err, application.ErrApplicationDeletionInProgress):
		return "The application is being deleted. Try again after deletion finishes."
	default:
		return "The backup operation could not be completed. Review the service state and try again."
	}
}

func (h *Handler) backupProgress(applicationID int64, serviceName, jobID string) (*backupProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.backupJobs.get(applicationID, serviceName, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	base := "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/" + url.PathEscape(serviceName) + "/backups"
	progress.StatusURL = base + "/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/services/" + url.PathEscape(serviceName) + "?tab=backups"
	return &progress, true
}

func (h *Handler) runBackupJob(ctx context.Context, job *backupJob) {
	var err error
	switch job.operation {
	case backupJobOperationRestore:
		err = h.backupManager.RestoreBackup(ctx, job.applicationID, job.serviceName, job.restoreFileName())
	default:
		_, err = h.backupManager.RunBackupNow(ctx, job.applicationID, job.serviceName)
	}
	if err != nil {
		job.fail(err)
		h.logger.Error("run backup operation", "application_id", job.applicationID, "service", job.serviceName, "operation", job.operation, "error", backupJobUserMessage(err))
		return
	}
	job.complete()
}

// restoreFileName is intentionally kept in the transient job object only. It
// is never serialized into the job store or exposed in a progress response.
func (j *backupJob) restoreFileName() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.restoreFile
}
