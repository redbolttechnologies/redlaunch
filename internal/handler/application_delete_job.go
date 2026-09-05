package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	applicationDeleteJobStateRunning  = "running"
	applicationDeleteJobStateComplete = "complete"
	applicationDeleteJobStateFailed   = "failed"

	applicationDeleteJobStepRemaining = "remaining"
	applicationDeleteJobStepActive    = "active"
	applicationDeleteJobStepComplete  = "complete"
	applicationDeleteJobStepFailed    = "failed"

	applicationDeleteJobRetention = time.Hour
)

type applicationDeleteJobStore struct {
	mu   sync.Mutex
	jobs map[string]*applicationDeleteJob
}

type applicationDeleteJob struct {
	mu sync.RWMutex

	id              string
	applicationID   int64
	applicationName string
	state           string
	currentStage    string
	errorStage      string
	errorDetail     string
	steps           []applicationDeleteJobStep
	finishedAt      time.Time
}

type applicationDeleteJobStep struct {
	Stage string
	Label string
	State string
}

type applicationDeleteProgressData struct {
	JobID           string
	ApplicationID   int64
	ApplicationName string
	State           string
	CurrentStage    string
	ErrorStage      string
	ErrorDetail     string
	StatusURL       string
	CloseURL        string
	Steps           []applicationDeleteJobStep
}

func newApplicationDeleteJobStore() *applicationDeleteJobStore {
	return &applicationDeleteJobStore{jobs: make(map[string]*applicationDeleteJob)}
}

func (s *applicationDeleteJobStore) create(applicationID int64, applicationName string) (*applicationDeleteJob, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, err
	}
	job := &applicationDeleteJob{
		id:              id,
		applicationID:   applicationID,
		applicationName: applicationName,
		state:           applicationDeleteJobStateRunning,
		steps:           applicationDeleteJobSteps(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > applicationDeleteJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, nil
}

func (s *applicationDeleteJobStore) get(applicationID int64, id string) *applicationDeleteJob {
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

func (j *applicationDeleteJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationDeleteJobStateRunning {
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
		j.currentStage = "Remove application resources"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == applicationDeleteJobStepRemaining || j.steps[index].State == applicationDeleteJobStepActive {
			j.steps[index].State = applicationDeleteJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = applicationDeleteJobStepActive
}

func (j *applicationDeleteJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationDeleteJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == applicationDeleteJobStepRemaining || j.steps[index].State == applicationDeleteJobStepActive {
			j.steps[index].State = applicationDeleteJobStepComplete
		}
	}
	j.state = applicationDeleteJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *applicationDeleteJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationDeleteJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == applicationDeleteJobStepActive {
			j.steps[index].State = applicationDeleteJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Remove application resources"
	}
	j.state = applicationDeleteJobStateFailed
	j.errorDetail = applicationDeletionUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *applicationDeleteJob) snapshot() applicationDeleteProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]applicationDeleteJobStep, len(j.steps))
	copy(steps, j.steps)
	return applicationDeleteProgressData{
		JobID:           j.id,
		ApplicationID:   j.applicationID,
		ApplicationName: j.applicationName,
		State:           j.state,
		CurrentStage:    j.currentStage,
		ErrorStage:      j.errorStage,
		ErrorDetail:     j.errorDetail,
		Steps:           steps,
	}
}

func applicationDeleteJobSteps() []applicationDeleteJobStep {
	return []applicationDeleteJobStep{
		{Stage: "resources", Label: "Remove application Docker resources", State: applicationDeleteJobStepRemaining},
		{Stage: "metadata", Label: "Delete application metadata", State: applicationDeleteJobStepRemaining},
		{Stage: "folder", Label: "Delete application folder", State: applicationDeleteJobStepRemaining},
	}
}

func (h *Handler) runApplicationDeleteJob(job *applicationDeleteJob) {
	var err error
	job.update("resources", "Removing application containers and Docker resources")
	if manager, ok := h.applicationDeletion.(applicationDeletionProgressService); ok {
		err = manager.DeleteApplicationWithProgress(context.Background(), job.applicationID, job.update)
	} else {
		err = h.applicationDeletion.DeleteApplication(context.Background(), job.applicationID)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("delete application", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", applicationDeletionUserMessage(err))
		return
	}
	job.complete()
}

func applicationDeletionUserMessage(err error) string {
	detail := ""
	if err != nil {
		detail = strings.ToLower(strings.TrimSpace(err.Error()))
	}
	switch {
	case strings.HasPrefix(detail, "remove application resources:"):
		return "The application's Docker resources could not be removed."
	case strings.HasPrefix(detail, "delete application metadata:"):
		return "The application's metadata could not be deleted."
	case strings.HasPrefix(detail, "delete application folder:"):
		return "The application folder could not be deleted."
	case errors.Is(err, application.ErrNotFound):
		return "The application could not be found. Refresh the page and try again."
	default:
		return "The application could not be deleted."
	}
}
