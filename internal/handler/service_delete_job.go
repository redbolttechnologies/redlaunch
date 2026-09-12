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
	serviceDeleteJobStateRunning  = "running"
	serviceDeleteJobStateComplete = "complete"
	serviceDeleteJobStateFailed   = "failed"

	serviceDeleteJobStepRemaining = "remaining"
	serviceDeleteJobStepActive    = "active"
	serviceDeleteJobStepComplete  = "complete"
	serviceDeleteJobStepFailed    = "failed"

	serviceDeleteJobRetention = time.Hour
)

type serviceDeletionProgressService interface {
	DeleteServiceWithProgress(context.Context, int64, string, func(stage, message string)) error
}

type serviceDeleteJobStore struct {
	mu   sync.Mutex
	jobs map[string]*serviceDeleteJob
}

type serviceDeleteJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	serviceName   string
	state         string
	currentStage  string
	errorStage    string
	errorDetail   string
	steps         []serviceDeleteJobStep
	finishedAt    time.Time
}

type serviceDeleteJobStep struct {
	Stage string
	Label string
	State string
}

type serviceDeleteProgressData struct {
	JobID         string
	ApplicationID int64
	ServiceName   string
	State         string
	CurrentStage  string
	ErrorStage    string
	ErrorDetail   string
	StatusURL     string
	CloseURL      string
	Steps         []serviceDeleteJobStep
}

func newServiceDeleteJobStore() *serviceDeleteJobStore {
	return &serviceDeleteJobStore{jobs: make(map[string]*serviceDeleteJob)}
}

func (s *serviceDeleteJobStore) create(applicationID int64, serviceName string) (*serviceDeleteJob, error) {
	job, _, err := s.createUnique(applicationID, serviceName)
	return job, err
}

func (s *serviceDeleteJobStore) createUnique(applicationID int64, serviceName string) (*serviceDeleteJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &serviceDeleteJob{
		id:            id,
		applicationID: applicationID,
		serviceName:   serviceName,
		state:         serviceDeleteJobStateRunning,
		steps:         serviceDeleteJobSteps(),
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
		if existingApplicationID == applicationID && existingServiceName == serviceName && state == serviceDeleteJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceDeleteJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *serviceDeleteJobStore) get(applicationID int64, id string) *serviceDeleteJob {
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

func (s *serviceDeleteJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceDeleteJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *serviceDeleteJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceDeleteJobStateRunning {
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
		j.currentStage = "Stop service container"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == serviceDeleteJobStepRemaining || j.steps[index].State == serviceDeleteJobStepActive {
			j.steps[index].State = serviceDeleteJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = serviceDeleteJobStepActive
}

func (j *serviceDeleteJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceDeleteJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == serviceDeleteJobStepRemaining || j.steps[index].State == serviceDeleteJobStepActive {
			j.steps[index].State = serviceDeleteJobStepComplete
		}
	}
	j.state = serviceDeleteJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *serviceDeleteJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceDeleteJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == serviceDeleteJobStepActive {
			j.steps[index].State = serviceDeleteJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Stop service container"
	}
	j.state = serviceDeleteJobStateFailed
	j.errorDetail = serviceDeletionUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *serviceDeleteJob) snapshot() serviceDeleteProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]serviceDeleteJobStep, len(j.steps))
	copy(steps, j.steps)
	return serviceDeleteProgressData{
		JobID:         j.id,
		ApplicationID: j.applicationID,
		ServiceName:   j.serviceName,
		State:         j.state,
		CurrentStage:  j.currentStage,
		ErrorStage:    j.errorStage,
		ErrorDetail:   j.errorDetail,
		Steps:         steps,
	}
}

func serviceDeleteJobSteps() []serviceDeleteJobStep {
	return []serviceDeleteJobStep{
		{Stage: "schedules", Label: "Disable scheduled backups", State: serviceDeleteJobStepRemaining},
		{Stage: "stop", Label: "Stop service container", State: serviceDeleteJobStepRemaining},
		{Stage: "remove", Label: "Remove service container", State: serviceDeleteJobStepRemaining},
		{Stage: "compose", Label: "Remove service from Compose file", State: serviceDeleteJobStepRemaining},
		{Stage: "routing", Label: "Remove service routing", State: serviceDeleteJobStepRemaining},
		{Stage: "metadata", Label: "Delete service metadata", State: serviceDeleteJobStepRemaining},
	}
}

func (h *Handler) runServiceDeleteJob(ctx context.Context, job *serviceDeleteJob) {
	var err error
	job.update("stop", "Stopping the service container with Docker Compose")
	if manager, ok := h.serviceDeletion.(serviceDeletionProgressService); ok {
		err = manager.DeleteServiceWithProgress(ctx, job.applicationID, job.serviceName, job.update)
	} else {
		err = h.serviceDeletion.DeleteService(ctx, job.applicationID, job.serviceName)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("delete service", "application_id", job.applicationID, "service", job.serviceName, "stage", snapshot.ErrorStage, "error", serviceDeletionUserMessage(err))
		return
	}
	job.complete()
}

func serviceDeletionUserMessage(err error) string {
	if errors.Is(err, application.ErrServiceNameRequired) || errors.Is(err, application.ErrServiceNameTooLong) || errors.Is(err, application.ErrServiceNameInvalid) {
		return "The service name is invalid."
	}
	if errors.Is(err, application.ErrApplicationDeletionInProgress) {
		return "The application is being deleted. Try again after deletion finishes."
	}

	detail := ""
	if err != nil {
		detail = strings.ToLower(strings.TrimSpace(err.Error()))
	}
	switch {
	case strings.HasPrefix(detail, "stop service:"):
		return "The service container could not be stopped."
	case strings.HasPrefix(detail, "remove service container:"):
		return "The service container could not be removed."
	case strings.HasPrefix(detail, "remove service from compose file:"):
		return "The service could not be removed from the Docker Compose file."
	case strings.HasPrefix(detail, "delete service metadata:"):
		return "The service container was removed, but its metadata could not be deleted."
	default:
		return "The service could not be deleted."
	}
}
