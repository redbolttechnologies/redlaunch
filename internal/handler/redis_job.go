package handler

import (
	"context"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	redisJobStateRunning  = "running"
	redisJobStateComplete = "complete"
	redisJobStateFailed   = "failed"

	redisJobStepRemaining = "remaining"
	redisJobStepActive    = "active"
	redisJobStepComplete  = "complete"
	redisJobStepFailed    = "failed"

	redisJobRetention = time.Hour
)

type redisProgressService interface {
	CreateRedisServiceWithProgress(context.Context, int64, application.RedisServiceInput, func(stage, message string)) (application.Service, error)
}

type redisServiceJobStore struct {
	mu   sync.Mutex
	jobs map[string]*redisServiceJob
}

type redisServiceJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	state         string
	currentStage  string
	errorStage    string
	errorDetail   string
	steps         []redisServiceJobStep
	finishedAt    time.Time
}

type redisServiceJobStep struct {
	Stage string
	Label string
	State string
}

type redisProgressData struct {
	JobID        string
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	StatusURL    string
	CloseURL     string
	Steps        []redisServiceJobStep
}

func newRedisServiceJobStore() *redisServiceJobStore {
	return &redisServiceJobStore{jobs: make(map[string]*redisServiceJob)}
}

func (s *redisServiceJobStore) create(applicationID int64) (*redisServiceJob, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, err
	}
	job := &redisServiceJob{
		id:            id,
		applicationID: applicationID,
		state:         redisJobStateRunning,
		steps:         redisServiceJobSteps(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > redisJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, nil
}

func (s *redisServiceJobStore) get(applicationID int64, id string) *redisServiceJob {
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

func (j *redisServiceJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != redisJobStateRunning {
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
		j.currentStage = "Create Redis service"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == redisJobStepRemaining || j.steps[index].State == redisJobStepActive {
			j.steps[index].State = redisJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = redisJobStepActive
}

func (j *redisServiceJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != redisJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == redisJobStepRemaining || j.steps[index].State == redisJobStepActive {
			j.steps[index].State = redisJobStepComplete
		}
	}
	j.state = redisJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *redisServiceJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != redisJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == redisJobStepActive {
			j.steps[index].State = redisJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Create Redis service"
	}
	j.state = redisJobStateFailed
	j.errorDetail = redisUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *redisServiceJob) snapshot() redisProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]redisServiceJobStep, len(j.steps))
	copy(steps, j.steps)
	return redisProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func redisServiceJobSteps() []redisServiceJobStep {
	return []redisServiceJobStep{
		{Stage: "configuration", Label: "Prepare Redis configuration", State: redisJobStepRemaining},
		{Stage: "files", Label: "Write Compose and environment files", State: redisJobStepRemaining},
		{Stage: "metadata", Label: "Save service metadata", State: redisJobStepRemaining},
		{Stage: "start", Label: "Start Redis container", State: redisJobStepRemaining},
	}
}

func (h *Handler) runRedisJob(job *redisServiceJob, input application.RedisServiceInput) {
	defer func() {
		input.Password = ""
	}()

	var err error
	if manager, ok := h.redisManager.(redisProgressService); ok {
		_, err = manager.CreateRedisServiceWithProgress(context.Background(), job.applicationID, input, job.update)
	} else {
		job.update("configuration", "Preparing Redis configuration")
		_, err = h.redisManager.CreateRedisService(context.Background(), job.applicationID, input)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("create Redis service", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", redisErrorDetail(err))
		return
	}
	job.complete()
}
