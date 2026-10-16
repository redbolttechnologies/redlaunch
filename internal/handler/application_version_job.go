package handler

import (
	"context"
	"errors"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	applicationVersionJobStateRunning  = "running"
	applicationVersionJobStateComplete = "complete"
	applicationVersionJobStateFailed   = "failed"

	applicationVersionJobRetention = time.Hour
)

// ErrApplicationVersionBusy reports that another version deployment is
// already running for the application. One key per application serializes
// version switches so concurrent tag moves cannot interleave.
var ErrApplicationVersionBusy = errors.New("another version deployment is already running for this application")

// applicationVersionDeployService deploys one registry tag to every eligible
// service of an application and reports per-service progress.
type applicationVersionDeployService interface {
	DeployApplicationVersion(context.Context, int64, string, func(serviceName, state, message string)) ([]application.VersionDeployOutcome, error)
}

type applicationVersionJobStore struct {
	mu   sync.Mutex
	jobs map[string]*applicationVersionJob
}

type applicationVersionJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	sourceTag     string
	state         string
	errorDetail   string
	steps         []applicationVersionJobStep
	finishedAt    time.Time
}

type applicationVersionJobStep struct {
	ServiceName string
	State       string
	Detail      string
}

type applicationVersionProgressData struct {
	JobID         string
	ApplicationID int64
	SourceTag     string
	State         string
	ErrorDetail   string
	Steps         []applicationVersionJobStep
	DeployedCount int
	SkippedCount  int
	FailedCount   int
	StatusURL     string
	CloseURL      string
}

func newApplicationVersionJobStore() *applicationVersionJobStore {
	return &applicationVersionJobStore{jobs: make(map[string]*applicationVersionJob)}
}

// createUnique returns the running job for the same source tag so a
// double-submitted form joins it, and refuses a different deployment while
// one is running.
func (s *applicationVersionJobStore) createUnique(applicationID int64, sourceTag string) (*applicationVersionJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &applicationVersionJob{
		id:            id,
		applicationID: applicationID,
		sourceTag:     sourceTag,
		state:         applicationVersionJobStateRunning,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		existingSourceTag := existing.sourceTag
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && state == applicationVersionJobStateRunning {
			if existingSourceTag == sourceTag {
				return existing, false, nil
			}
			return nil, false, ErrApplicationVersionBusy
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > applicationVersionJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *applicationVersionJobStore) get(applicationID int64, id string) *applicationVersionJob {
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

func (s *applicationVersionJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > applicationVersionJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

// update records one service's deployment progress. Steps are appended as the
// services are discovered so skipped services stay visible in the summary.
func (j *applicationVersionJob) update(serviceName, state, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationVersionJobStateRunning {
		return
	}
	stepIndex := -1
	for index := range j.steps {
		if j.steps[index].ServiceName == serviceName {
			stepIndex = index
			break
		}
	}
	if stepIndex == -1 {
		j.steps = append(j.steps, applicationVersionJobStep{ServiceName: serviceName})
		stepIndex = len(j.steps) - 1
	}
	j.steps[stepIndex].State = versionDeployStepState(state)
	j.steps[stepIndex].Detail = message
}

func versionDeployStepState(state string) string {
	switch state {
	case application.VersionDeployStateActive:
		return applicationContainerJobStepActive
	case application.VersionDeployStateDeployed:
		return applicationContainerJobStepComplete
	case application.VersionDeployStateSkipped:
		return "skipped"
	default:
		return applicationContainerJobStepFailed
	}
}

func (j *applicationVersionJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationVersionJobStateRunning {
		return
	}
	j.state = applicationVersionJobStateComplete
	j.finishedAt = time.Now()
}

func (j *applicationVersionJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != applicationVersionJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == applicationContainerJobStepActive {
			j.steps[index].State = applicationContainerJobStepFailed
		}
	}
	j.state = applicationVersionJobStateFailed
	j.errorDetail = versionDeployUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *applicationVersionJob) snapshot() applicationVersionProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]applicationVersionJobStep, len(j.steps))
	copy(steps, j.steps)
	progress := applicationVersionProgressData{
		JobID:         j.id,
		ApplicationID: j.applicationID,
		SourceTag:     j.sourceTag,
		State:         j.state,
		ErrorDetail:   j.errorDetail,
		Steps:         steps,
	}
	for _, step := range steps {
		switch step.State {
		case applicationContainerJobStepComplete:
			progress.DeployedCount++
		case "skipped":
			progress.SkippedCount++
		case applicationContainerJobStepFailed:
			progress.FailedCount++
		}
	}
	return progress
}

func (h *Handler) runApplicationVersionJob(ctx context.Context, job *applicationVersionJob, deployer applicationVersionDeployService) {
	_, err := deployer.DeployApplicationVersion(ctx, job.applicationID, job.sourceTag, job.update)
	if err != nil {
		job.fail(err)
		h.logger.Error("deploy application version",
			"application_id", job.applicationID,
			"source_tag", job.sourceTag,
			"error", serviceActionErrorDetail(err))
		return
	}
	job.complete()
}

// versionDeployUserMessage maps version deployment failures onto short
// operator-facing text. Docker diagnostics never reach the dialog.
func versionDeployUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrVersionTagRequired):
		return "Choose a version tag to deploy."
	case errors.Is(err, application.ErrRegistryRepositoryInvalid):
		return "The version tag is not a valid registry tag."
	case errors.Is(err, application.ErrServiceVersionUnavailable):
		return "No service deploys images through the local image registry."
	case errors.Is(err, application.ErrRegistryUnavailable):
		return "The local image registry is unavailable."
	case errors.Is(err, application.ErrVersionImageAbsent):
		return "The repository does not contain the selected image."
	default:
		return "The version deployment stopped unexpectedly. Inspect the services before retrying."
	}
}
