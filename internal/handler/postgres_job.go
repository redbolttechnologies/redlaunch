package handler

import (
	"context"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	postgresJobStateRunning  = "running"
	postgresJobStateComplete = "complete"
	postgresJobStateFailed   = "failed"

	postgresJobStepRemaining = "remaining"
	postgresJobStepActive    = "active"
	postgresJobStepComplete  = "complete"
	postgresJobStepFailed    = "failed"

	postgresJobRetention = time.Hour
)

type postgresqlProgressService interface {
	CreatePostgreSQLServiceWithProgress(context.Context, int64, application.PostgreSQLServiceInput, func(stage, message string)) (application.Service, error)
}

type postgresJobStore struct {
	mu   sync.Mutex
	jobs map[string]*postgresJob
}

type postgresJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	state         string
	currentStage  string
	errorStage    string
	errorDetail   string
	steps         []postgresJobStep
	finishedAt    time.Time
}

type postgresJobStep struct {
	Stage string
	Label string
	State string
}

type postgresProgressData struct {
	JobID        string
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	StatusURL    string
	CloseURL     string
	Steps        []postgresJobStep
}

func newPostgresJobStore() *postgresJobStore {
	return &postgresJobStore{jobs: make(map[string]*postgresJob)}
}

func (s *postgresJobStore) create(applicationID int64) (*postgresJob, error) {
	job, _, err := s.createUnique(applicationID)
	return job, err
}

func (s *postgresJobStore) createUnique(applicationID int64) (*postgresJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &postgresJob{
		id:            id,
		applicationID: applicationID,
		state:         postgresJobStateRunning,
		steps:         postgresJobSteps(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && state == postgresJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > postgresJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *postgresJobStore) get(applicationID int64, id string) *postgresJob {
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

func (s *postgresJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > postgresJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *postgresJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != postgresJobStateRunning {
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
		j.currentStage = "Create PostgreSQL database"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == postgresJobStepRemaining || j.steps[index].State == postgresJobStepActive {
			j.steps[index].State = postgresJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = postgresJobStepActive
}

func (j *postgresJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != postgresJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == postgresJobStepRemaining || j.steps[index].State == postgresJobStepActive {
			j.steps[index].State = postgresJobStepComplete
		}
	}
	j.state = postgresJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *postgresJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != postgresJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == postgresJobStepActive {
			j.steps[index].State = postgresJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Create PostgreSQL database"
	}
	j.state = postgresJobStateFailed
	j.errorDetail = postgresUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *postgresJob) snapshot() postgresProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]postgresJobStep, len(j.steps))
	copy(steps, j.steps)
	return postgresProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func postgresJobSteps() []postgresJobStep {
	return []postgresJobStep{
		{Stage: "configuration", Label: "Prepare PostgreSQL configuration", State: postgresJobStepRemaining},
		{Stage: "files", Label: "Write Compose and environment files", State: postgresJobStepRemaining},
		{Stage: "metadata", Label: "Save service metadata", State: postgresJobStepRemaining},
		{Stage: "start", Label: "Start PostgreSQL container", State: postgresJobStepRemaining},
	}
}

func (h *Handler) runPostgresJob(ctx context.Context, job *postgresJob, input application.PostgreSQLServiceInput) {
	defer func() {
		input.DatabasePassword = ""
	}()

	var err error
	if manager, ok := h.postgresManager.(postgresqlProgressService); ok {
		_, err = manager.CreatePostgreSQLServiceWithProgress(ctx, job.applicationID, input, job.update)
	} else {
		job.update("configuration", "Preparing PostgreSQL configuration")
		_, err = h.postgresManager.CreatePostgreSQLService(ctx, job.applicationID, input)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("create PostgreSQL service", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", postgresErrorDetail(err))
		return
	}
	job.complete()
}
