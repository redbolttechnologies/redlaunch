package handler

import (
	"sync"
	"time"
)

const (
	apiRunJobStateRunning  = "running"
	apiRunJobStateComplete = "complete"
	apiRunJobStateFailed   = "failed"

	apiRunJobRetention = time.Hour
)

// apiRunJobStore tracks machine-triggered one-off service runs. Like the
// backup job store it is process-local: jobs do not survive a manager
// restart, and callers re-dispatch after a restart instead of resuming.
type apiRunJobStore struct {
	mu   sync.Mutex
	jobs map[string]*apiRunJob
}

type apiRunJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	serviceName   string
	state         string
	errorDetail   string
	operationErr  error
	finishedAt    time.Time
	done          chan struct{}
	doneOnce      sync.Once
}

func newAPIRunJobStore() *apiRunJobStore {
	return &apiRunJobStore{jobs: make(map[string]*apiRunJob)}
}

func (s *apiRunJobStore) createUnique(applicationID int64, serviceName string) (*apiRunJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &apiRunJob{
		id:            id,
		applicationID: applicationID,
		serviceName:   serviceName,
		state:         apiRunJobStateRunning,
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
		if existingApplicationID == applicationID && existingServiceName == serviceName && state == apiRunJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > apiRunJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *apiRunJobStore) get(applicationID int64, serviceName, id string) *apiRunJob {
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
	if !finishedAt.IsZero() && time.Since(finishedAt) > apiRunJobRetention {
		delete(s.jobs, id)
		return nil
	}
	return job
}

func (s *apiRunJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > apiRunJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *apiRunJob) complete() {
	j.mu.Lock()
	if j.state == apiRunJobStateRunning {
		j.state = apiRunJobStateComplete
		j.finishedAt = time.Now()
	}
	j.mu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
}

func (j *apiRunJob) fail(err error) {
	j.mu.Lock()
	if j.state == apiRunJobStateRunning {
		j.state = apiRunJobStateFailed
		j.operationErr = err
		j.errorDetail = serviceActionErrorDetail(err)
		j.finishedAt = time.Now()
	}
	j.mu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
}

func (j *apiRunJob) snapshot() (state, detail string) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.state, j.errorDetail
}
