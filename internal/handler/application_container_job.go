package handler

import (
	"context"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	applicationContainerJobStateRunning  = "running"
	applicationContainerJobStateComplete = "complete"
	applicationContainerJobStateFailed   = "failed"

	applicationContainerJobStepRemaining = "remaining"
	applicationContainerJobStepActive    = "active"
	applicationContainerJobStepComplete  = "complete"
	applicationContainerJobStepFailed    = "failed"

	applicationContainerJobRetention = time.Hour
)

type applicationContainerProgressService interface {
	CreateApplicationServiceWithProgress(context.Context, int64, application.ApplicationServiceInput, func(stage, message string)) (application.Service, error)
}

type applicationContainerJobStore struct {
	mu   sync.Mutex
	jobs map[string]*applicationContainerJob
}

type applicationContainerJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	autoStart     bool
	state         string
	currentStage  string
	errorStage    string
	errorDetail   string
	steps         []applicationContainerJobStep
	finishedAt    time.Time
}

type applicationContainerJobStep struct {
	Stage string
	Label string
	State string
}

type applicationContainerProgressData struct {
	JobID        string
	AutoStart    bool
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	StatusURL    string
	CloseURL     string
	Steps        []applicationContainerJobStep
}

func newApplicationContainerJobStore() *applicationContainerJobStore {
	return &applicationContainerJobStore{jobs: make(map[string]*applicationContainerJob)}
}

func (s *applicationContainerJobStore) create(applicationID int64, autoStart bool) (*applicationContainerJob, error) {
	job, _, err := s.createUnique(applicationID, autoStart)
	return job, err
}

func (s *applicationContainerJobStore) createUnique(applicationID int64, autoStart bool) (*applicationContainerJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &applicationContainerJob{
		id:            id,
		applicationID: applicationID,
		autoStart:     autoStart,
		state:         applicationContainerJobStateRunning,
		steps:         applicationContainerJobSteps(autoStart),
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
		if existingApplicationID == applicationID && state == applicationContainerJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > applicationContainerJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *applicationContainerJobStore) get(applicationID int64, id string) *applicationContainerJob {
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

func (s *applicationContainerJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > applicationContainerJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *applicationContainerJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationContainerJobStateRunning {
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
		j.currentStage = "Create application container"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == applicationContainerJobStepRemaining || j.steps[index].State == applicationContainerJobStepActive {
			j.steps[index].State = applicationContainerJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = applicationContainerJobStepActive
}

func (j *applicationContainerJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationContainerJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == applicationContainerJobStepRemaining || j.steps[index].State == applicationContainerJobStepActive {
			j.steps[index].State = applicationContainerJobStepComplete
		}
	}
	j.state = applicationContainerJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *applicationContainerJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationContainerJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == applicationContainerJobStepActive {
			j.steps[index].State = applicationContainerJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Create application container"
	}
	j.state = applicationContainerJobStateFailed
	j.errorDetail = applicationContainerUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *applicationContainerJob) snapshot() applicationContainerProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]applicationContainerJobStep, len(j.steps))
	copy(steps, j.steps)
	return applicationContainerProgressData{
		JobID:        j.id,
		AutoStart:    j.autoStart,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func applicationContainerJobSteps(autoStart bool) []applicationContainerJobStep {
	steps := []applicationContainerJobStep{
		{Stage: "configuration", Label: "Prepare application container configuration", State: applicationContainerJobStepRemaining},
		{Stage: "files", Label: "Write Compose and environment files", State: applicationContainerJobStepRemaining},
		{Stage: "metadata", Label: "Save service metadata", State: applicationContainerJobStepRemaining},
	}
	if autoStart {
		steps = append(steps, applicationContainerJobStep{Stage: "start", Label: "Start application container", State: applicationContainerJobStepRemaining})
	}
	return steps
}

func (h *Handler) runApplicationContainerJob(ctx context.Context, job *applicationContainerJob, input application.ApplicationServiceInput) {
	var err error
	if manager, ok := h.applicationContainerManager.(applicationContainerProgressService); ok {
		_, err = manager.CreateApplicationServiceWithProgress(ctx, job.applicationID, input, job.update)
	} else {
		job.update("configuration", "Preparing application container configuration")
		_, err = h.applicationContainerManager.CreateApplicationService(ctx, job.applicationID, input)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("create application container", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", applicationContainerErrorDetail(err))
		return
	}
	job.complete()
}
