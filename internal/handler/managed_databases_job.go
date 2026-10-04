package handler

import (
	"context"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	managedDatabasesJobStateRunning  = "running"
	managedDatabasesJobStateComplete = "complete"
	managedDatabasesJobStateFailed   = "failed"

	managedDatabasesJobStepRemaining = "remaining"
	managedDatabasesJobStepActive    = "active"
	managedDatabasesJobStepComplete  = "complete"
	managedDatabasesJobStepFailed    = "failed"

	managedDatabasesJobRetention = time.Hour
)

type managedDatabasesProgressService interface {
	EnableClusterWithProgress(context.Context, application.ManagedDatabaseEnableInput, func(stage, message string)) error
}

type managedDatabasesJobStore struct {
	mu   sync.Mutex
	jobs map[string]*managedDatabasesJob
}

type managedDatabasesJob struct {
	mu sync.RWMutex

	id           string
	state        string
	currentStage string
	errorStage   string
	errorDetail  string
	steps        []managedDatabasesJobStep
	finishedAt   time.Time
}

type managedDatabasesJobStep struct {
	Stage string
	Label string
	State string
}

type managedDatabasesProgressData struct {
	JobID        string
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	StatusURL    string
	CloseURL     string
	Steps        []managedDatabasesJobStep
}

func newManagedDatabasesJobStore() *managedDatabasesJobStore {
	return &managedDatabasesJobStore{jobs: make(map[string]*managedDatabasesJob)}
}

func (s *managedDatabasesJobStore) createUnique() (*managedDatabasesJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &managedDatabasesJob{
		id:    id,
		state: managedDatabasesJobStateRunning,
		steps: managedDatabasesJobSteps(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if state == managedDatabasesJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > managedDatabasesJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *managedDatabasesJobStore) get(id string) *managedDatabasesJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

func (s *managedDatabasesJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > managedDatabasesJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *managedDatabasesJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != managedDatabasesJobStateRunning {
		return
	}
	stepIndex := -1
	for index := range j.steps {
		if j.steps[index].Stage == stage {
			stepIndex = index
			break
		}
	}
	if stepIndex == -1 {
		j.currentStage = "Enable managed databases"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == managedDatabasesJobStepRemaining || j.steps[index].State == managedDatabasesJobStepActive {
			j.steps[index].State = managedDatabasesJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = managedDatabasesJobStepActive
}

func (j *managedDatabasesJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != managedDatabasesJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == managedDatabasesJobStepRemaining || j.steps[index].State == managedDatabasesJobStepActive {
			j.steps[index].State = managedDatabasesJobStepComplete
		}
	}
	j.state = managedDatabasesJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *managedDatabasesJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != managedDatabasesJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == managedDatabasesJobStepActive {
			j.steps[index].State = managedDatabasesJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Enable managed databases"
	}
	j.state = managedDatabasesJobStateFailed
	j.errorDetail = managedDatabasesUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *managedDatabasesJob) snapshot() managedDatabasesProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]managedDatabasesJobStep, len(j.steps))
	copy(steps, j.steps)
	return managedDatabasesProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func managedDatabasesJobSteps() []managedDatabasesJobStep {
	return []managedDatabasesJobStep{
		{Stage: "configuration", Label: "Prepare managed database configuration", State: managedDatabasesJobStepRemaining},
		{Stage: "files", Label: "Write Compose and environment files", State: managedDatabasesJobStepRemaining},
		{Stage: "metadata", Label: "Save managed database configuration", State: managedDatabasesJobStepRemaining},
		{Stage: "start", Label: "Start managed database container", State: managedDatabasesJobStepRemaining},
	}
}

func (h *Handler) runManagedDatabasesJob(ctx context.Context, job *managedDatabasesJob, input application.ManagedDatabaseEnableInput) {
	defer func() {
		input.Password = ""
	}()
	var err error
	if manager, ok := h.managedDatabases.(managedDatabasesProgressService); ok {
		err = manager.EnableClusterWithProgress(ctx, input, job.update)
	} else if h.managedDatabases != nil {
		job.update("configuration", "Preparing managed database configuration")
		err = h.managedDatabases.EnableCluster(ctx, input)
	} else {
		err = errManagedDatabasesNotConfigured
	}
	if err != nil {
		job.fail(err)
		h.logger.Error("enable managed databases", "error", managedDatabasesErrorDetail(err))
		return
	}
	job.complete()
}
