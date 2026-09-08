package handler

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	githubActionsJobOperationConfigure = "configure"
	githubActionsJobOperationRevoke    = "revoke"

	githubActionsJobStateRunning  = "running"
	githubActionsJobStateComplete = "complete"
	githubActionsJobStateFailed   = "failed"

	githubActionsJobStepRemaining = "remaining"
	githubActionsJobStepActive    = "active"
	githubActionsJobStepComplete  = "complete"
	githubActionsJobStepFailed    = "failed"

	githubActionsJobRetention = time.Hour
	githubActionsJobTimeout   = 15 * time.Minute
)

type githubActionsProgressService interface {
	ConfigureWithProgress(context.Context, int64, application.GitHubActionsInput, func(stage, message string)) (application.GitHubActionsSetup, error)
}

type githubActionsRevokeProgressService interface {
	RevokeWithProgress(context.Context, int64, func(stage, message string)) error
}

type githubActionsJobStore struct {
	mu   sync.Mutex
	jobs map[string]*githubActionsJob
}

type githubActionsJob struct {
	mu sync.RWMutex

	id            string
	applicationID int64
	operation     string
	state         string
	currentStage  string
	errorStage    string
	errorDetail   string
	steps         []githubActionsJobStep
	handoff       *application.GitHubActionsSetup
	finishedAt    time.Time
}

type githubActionsJobStep struct {
	Stage string
	Label string
	State string
}

type githubActionsProgressData struct {
	JobID               string
	State               string
	CurrentStage        string
	ErrorStage          string
	ErrorDetail         string
	StatusURL           string
	CloseURL            string
	HandoffURL          string
	Title               string
	CompleteTitle       string
	FailedTitle         string
	CompleteDescription string
	CompleteAction      string
	Steps               []githubActionsJobStep
}

func newGitHubActionsJobStore() *githubActionsJobStore {
	return &githubActionsJobStore{jobs: make(map[string]*githubActionsJob)}
}

func (s *githubActionsJobStore) create(applicationID int64, operation string) (*githubActionsJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &githubActionsJob{
		id:            id,
		applicationID: applicationID,
		operation:     operation,
		state:         githubActionsJobStateRunning,
		steps:         githubActionsJobSteps(operation),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		existingState := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && existingState == githubActionsJobStateRunning {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > githubActionsJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *githubActionsJobStore) get(applicationID int64, id string) *githubActionsJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	job.mu.RLock()
	belongsToApplication := job.applicationID == applicationID
	finishedAt := job.finishedAt
	job.mu.RUnlock()
	if !belongsToApplication {
		return nil
	}
	if !finishedAt.IsZero() && time.Since(finishedAt) > githubActionsJobRetention {
		delete(s.jobs, id)
		return nil
	}
	return job
}

func (j *githubActionsJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != githubActionsJobStateRunning {
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
		j.currentStage = "Configure GitHub Actions"
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == githubActionsJobStepRemaining || j.steps[index].State == githubActionsJobStepActive {
			j.steps[index].State = githubActionsJobStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = githubActionsJobStepActive
}

func (j *githubActionsJob) complete(setup application.GitHubActionsSetup) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != githubActionsJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == githubActionsJobStepRemaining || j.steps[index].State == githubActionsJobStepActive {
			j.steps[index].State = githubActionsJobStepComplete
		}
	}
	setup.Instructions = append([]string(nil), setup.Instructions...)
	j.handoff = &setup
	j.state = githubActionsJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *githubActionsJob) completeRevoke() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != githubActionsJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == githubActionsJobStepRemaining || j.steps[index].State == githubActionsJobStepActive {
			j.steps[index].State = githubActionsJobStepComplete
		}
	}
	j.state = githubActionsJobStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *githubActionsJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != githubActionsJobStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == githubActionsJobStepActive {
			j.steps[index].State = githubActionsJobStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Validate settings"
	}
	j.state = githubActionsJobStateFailed
	j.errorDetail = githubActionsUserMessage(err)
	j.finishedAt = time.Now()
}

func (j *githubActionsJob) snapshot() githubActionsProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]githubActionsJobStep, len(j.steps))
	copy(steps, j.steps)
	progress := githubActionsProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
	if j.operation == githubActionsJobOperationRevoke {
		progress.Title = "Revoking GitHub Actions access"
		progress.CompleteTitle = "Repository key revoked"
		progress.FailedTitle = "GitHub Actions revocation stopped"
		progress.CompleteDescription = "The repository key has been removed from the SSH gateway and can no longer open a registry tunnel."
		progress.CompleteAction = "Close"
	} else {
		progress.Title = "Setting up GitHub Actions"
		progress.CompleteTitle = "SSH gateway ready"
		progress.FailedTitle = "GitHub Actions setup stopped"
		progress.CompleteDescription = "The generated private key is ready. Open the handoff page to copy it into GitHub before leaving this setup."
		progress.CompleteAction = "Open GitHub handoff"
	}
	return progress
}

func (j *githubActionsJob) setup() (application.GitHubActionsSetup, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.handoff == nil || j.state != githubActionsJobStateComplete {
		return application.GitHubActionsSetup{}, false
	}
	setup := *j.handoff
	setup.Instructions = append([]string(nil), j.handoff.Instructions...)
	return setup, true
}

func githubActionsJobSteps(operation string) []githubActionsJobStep {
	if operation == githubActionsJobOperationRevoke {
		return []githubActionsJobStep{
			{Stage: "gateway", Label: "Prepare SSH gateway", State: githubActionsJobStepRemaining},
			{Stage: "authorization", Label: "Remove repository key", State: githubActionsJobStepRemaining},
			{Stage: "gateway-start", Label: "Restart SSH gateway", State: githubActionsJobStepRemaining},
			{Stage: "metadata", Label: "Remove integration metadata", State: githubActionsJobStepRemaining},
		}
	}
	return []githubActionsJobStep{
		{Stage: "validation", Label: "Validate settings", State: githubActionsJobStepRemaining},
		{Stage: "registry", Label: "Start Docker Registry", State: githubActionsJobStepRemaining},
		{Stage: "gateway", Label: "Prepare SSH gateway", State: githubActionsJobStepRemaining},
		{Stage: "credentials", Label: "Generate SSH credentials", State: githubActionsJobStepRemaining},
		{Stage: "authorization", Label: "Install key restrictions", State: githubActionsJobStepRemaining},
		{Stage: "gateway-start", Label: "Build and start SSH gateway", State: githubActionsJobStepRemaining},
		{Stage: "metadata", Label: "Save integration", State: githubActionsJobStepRemaining},
	}
}

func (h *Handler) startGitHubActionsConfigureJob(job *githubActionsJob, input application.GitHubActionsInput) {
	h.githubActionsJobWorkers.Add(1)
	go func() {
		defer h.githubActionsJobWorkers.Done()
		ctx, cancel := context.WithTimeout(h.githubActionsJobContext, githubActionsJobTimeout)
		defer cancel()
		h.runGitHubActionsConfigureJob(ctx, job, input)
	}()
}

func (h *Handler) runGitHubActionsConfigureJob(ctx context.Context, job *githubActionsJob, input application.GitHubActionsInput) {
	var (
		setup application.GitHubActionsSetup
		err   error
	)
	if manager, ok := h.githubActions.(githubActionsProgressService); ok {
		setup, err = manager.ConfigureWithProgress(ctx, job.applicationID, input, job.update)
	} else {
		job.update("validation", "Checking the application and workflow settings")
		setup, err = h.githubActions.Configure(ctx, job.applicationID, input)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("configure GitHub Actions", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", err)
		return
	}
	job.complete(setup)
}

func (h *Handler) startGitHubActionsRevokeJob(job *githubActionsJob) {
	h.githubActionsJobWorkers.Add(1)
	go func() {
		defer h.githubActionsJobWorkers.Done()
		ctx, cancel := context.WithTimeout(h.githubActionsJobContext, githubActionsJobTimeout)
		defer cancel()
		h.runGitHubActionsRevokeJob(ctx, job)
	}()
}

func (h *Handler) runGitHubActionsRevokeJob(ctx context.Context, job *githubActionsJob) {
	var err error
	if manager, ok := h.githubActions.(githubActionsRevokeProgressService); ok {
		err = manager.RevokeWithProgress(ctx, job.applicationID, job.update)
	} else {
		job.update("authorization", "Removing the repository key")
		err = h.githubActions.Revoke(ctx, job.applicationID)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("revoke GitHub Actions", "application_id", job.applicationID, "stage", snapshot.ErrorStage, "error", err)
		return
	}
	job.completeRevoke()
}

// Shutdown cancels in-flight GitHub Actions provisioning jobs and waits for
// their workers before infrastructure dependencies such as SQLite are closed.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.githubActionsJobCancel()
	done := make(chan struct{})
	go func() {
		h.githubActionsJobWorkers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) githubActionsProgress(applicationID int64, jobID string) (*githubActionsProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.githubActionsJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	applicationPath := "/applications/" + strconv.FormatInt(applicationID, 10) + "/deployments/github-actions"
	progress.StatusURL = applicationPath + "/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = applicationPath
	job.mu.RLock()
	operation := job.operation
	job.mu.RUnlock()
	if operation == githubActionsJobOperationConfigure {
		progress.HandoffURL = applicationPath + "?github_actions_job=" + url.QueryEscape(jobID) + "&github_actions_handoff=1"
	}
	return &progress, true
}
