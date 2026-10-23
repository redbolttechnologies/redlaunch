package handler

import (
	"context"
	"errors"
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	serviceActionJobStateRunning  = "running"
	serviceActionJobStateComplete = "complete"
	serviceActionJobStateFailed   = "failed"

	serviceActionJobRetention = time.Hour
)

// ErrServiceActionBusy reports that another operation is already running for
// the same service. One key per service serializes start/stop/restart/run so
// concurrent Docker operations cannot interleave.
var ErrServiceActionBusy = errors.New("another operation is already running for this service")

type serviceActionJobStore struct {
	mu   sync.Mutex
	jobs map[string]*serviceActionJob
}

type serviceActionJob struct {
	mu sync.RWMutex

	id             string
	applicationID  int64
	serviceName    string
	operation      string
	credentialUser string
	credentialPass string
	hasCredentials bool
	deployDigest   string
	stage          string
	state          string
	errorDetail    string
	errorTechnical string
	finishedAt     time.Time

	parameters    application.OneOffRunParameters
	hasParameters bool
	runResult     application.OneOffRunResult
	hasRunResult  bool
}

type serviceActionProgressData struct {
	JobID           string
	ApplicationID   int64
	ServiceName     string
	Operation       string
	State           string
	Detail          string
	ErrorDetail     string
	ErrorTechnical  string
	StatusURL       string
	CloseURL        string
	ExitCode        int
	OutputAvailable bool
	OutputTruncated bool
	OutputURL       string
	OutputDialogURL string
}

func newServiceActionJobStore() *serviceActionJobStore {
	return &serviceActionJobStore{jobs: make(map[string]*serviceActionJob)}
}

func (s *serviceActionJobStore) createUnique(applicationID int64, serviceName, operation string) (*serviceActionJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &serviceActionJob{
		id:            id,
		applicationID: applicationID,
		serviceName:   serviceName,
		operation:     operation,
		state:         serviceActionJobStateRunning,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		existingApplicationID := existing.applicationID
		existingServiceName := existing.serviceName
		existingOperation := existing.operation
		state := existing.state
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if existingApplicationID == applicationID && existingServiceName == serviceName && state == serviceActionJobStateRunning {
			if existingOperation == operation {
				return existing, false, nil
			}
			return nil, false, ErrServiceActionBusy
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *serviceActionJobStore) get(applicationID int64, serviceName, id string) *serviceActionJob {
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
	if !finishedAt.IsZero() && time.Since(finishedAt) > serviceActionJobRetention {
		delete(s.jobs, id)
		return nil
	}
	return job
}

func (s *serviceActionJobStore) getByID(applicationID int64, id string) *serviceActionJob {
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
	if !finishedAt.IsZero() && time.Since(finishedAt) > serviceActionJobRetention {
		delete(s.jobs, id)
		return nil
	}
	return job
}

// active returns running jobs for one application so pages can render
// persistent non-blocking toasts even after navigation drops the ?job= param.
func (s *serviceActionJobStore) active(applicationID int64) []*serviceActionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []*serviceActionJob
	for _, job := range s.jobs {
		job.mu.RLock()
		matches := job.applicationID == applicationID && job.state == serviceActionJobStateRunning
		job.mu.RUnlock()
		if matches {
			running = append(running, job)
		}
	}
	return running
}

func (s *serviceActionJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > serviceActionJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *serviceActionJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceActionJobStateRunning {
		return
	}
	j.state = serviceActionJobStateComplete
	j.finishedAt = time.Now()
}

func (j *serviceActionJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceActionJobStateRunning {
		return
	}
	j.state = serviceActionJobStateFailed
	// Preserve the same user-facing diagnostics as the former synchronous
	// error dialog (port-conflict hint + redacted Compose tail).
	dialog := newServiceActionError(j.operation, j.serviceName, err)
	j.errorDetail = dialog.Message
	j.errorTechnical = dialog.Detail
	j.finishedAt = time.Now()
}

func (j *serviceActionJob) snapshot() serviceActionProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return serviceActionProgressData{
		JobID:           j.id,
		ApplicationID:   j.applicationID,
		ServiceName:     j.serviceName,
		Operation:       j.operation,
		State:           j.state,
		Detail:          j.stage,
		ErrorDetail:     j.errorDetail,
		ErrorTechnical:  j.errorTechnical,
		ExitCode:        j.runResult.ExitCode,
		OutputAvailable: j.hasRunResult,
		OutputTruncated: j.runResult.Truncated,
	}
}

// setParameters attaches validated per-run values before the job starts. The
// map is copied so later form parsing cannot mutate a running job.
func (j *serviceActionJob) setParameters(parameters application.OneOffRunParameters) {
	j.mu.Lock()
	defer j.mu.Unlock()
	env := make(map[string]string, len(parameters.Env))
	for key, value := range parameters.Env {
		env[key] = value
	}
	j.parameters = application.OneOffRunParameters{
		Args: append([]string(nil), parameters.Args...),
		Env:  env,
	}
	j.hasParameters = true
}

func (j *serviceActionJob) takeParameters() application.OneOffRunParameters {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if !j.hasParameters {
		return application.OneOffRunParameters{}
	}
	env := make(map[string]string, len(j.parameters.Env))
	for key, value := range j.parameters.Env {
		env[key] = value
	}
	return application.OneOffRunParameters{
		Args: append([]string(nil), j.parameters.Args...),
		Env:  env,
	}
}

// setRunResult records captured output even when the run failed. The job owns
// the strings until it expires, which keeps output in memory for one hour.
func (j *serviceActionJob) setRunResult(result application.OneOffRunResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.runResult = result
	j.hasRunResult = true
}

func (j *serviceActionJob) runResultSnapshot() (application.OneOffRunResult, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.runResult, j.hasRunResult
}

// setDeployTarget attaches the selected image digest to a version deployment
// job. The value is validated before the job starts and is never secret.
func (j *serviceActionJob) setDeployTarget(digest string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.deployDigest = digest
}

func (j *serviceActionJob) takeDeployTarget() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.deployDigest
}

// setStage records the running stage so the toast can show what the version
// deployment is doing while pulls and recreations run.
func (j *serviceActionJob) setStage(_, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != serviceActionJobStateRunning {
		return
	}
	j.stage = message
}

// setCredentials attaches the desired database credentials to a credentials
// job. The values live only in process memory and are wiped when the job
// finishes; they never reach logs or templates.
func (j *serviceActionJob) setCredentials(user, password string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.credentialUser = user
	j.credentialPass = password
	j.hasCredentials = true
}

func (j *serviceActionJob) takeCredentials() (string, string, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.credentialUser, j.credentialPass, j.hasCredentials
}

func (j *serviceActionJob) wipeCredentials() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.credentialUser = ""
	j.credentialPass = ""
	j.hasCredentials = false
}

func (h *Handler) runServiceActionJob(ctx context.Context, job *serviceActionJob) {
	var err error
	switch job.operation {
	case "start":
		err = h.serviceActions.StartService(ctx, job.applicationID, job.serviceName)
	case "stop":
		err = h.serviceActions.StopService(ctx, job.applicationID, job.serviceName)
	case "restart":
		err = h.serviceActions.RestartService(ctx, job.applicationID, job.serviceName)
	case "run":
		result, runErr := h.serviceActions.RunServiceOnceWithParameters(ctx, job.applicationID, job.serviceName, job.takeParameters())
		job.setRunResult(result)
		err = runErr
		h.logger.Info("ran one-off service action",
			"application_id", job.applicationID,
			"service", job.serviceName,
			"job_id", job.id,
			"exit_code", result.ExitCode,
			"stdout_bytes", len(result.Stdout),
			"stderr_bytes", len(result.Stderr),
			"truncated", result.Truncated)
	case "deploy":
		if h.versionDeploys == nil {
			err = errors.New("version deployment service is not configured")
			break
		}
		err = h.versionDeploys.DeployServiceVersion(ctx, job.applicationID, job.serviceName, job.takeDeployTarget(), job.setStage)
	case "credentials":
		err = h.runPostgresCredentialsJob(ctx, job)
	default:
		err = errors.New("unknown service operation")
	}
	if err != nil {
		job.fail(err)
		if job.operation == "run" {
			result, _ := job.runResultSnapshot()
			h.logger.Error("run service action job",
				"application_id", job.applicationID,
				"service", job.serviceName,
				"job_id", job.id,
				"exit_code", result.ExitCode,
				"stdout_bytes", len(result.Stdout),
				"stderr_bytes", len(result.Stderr),
				"truncated", result.Truncated)
			return
		}
		h.logger.Error("run service action job",
			"application_id", job.applicationID,
			"service", job.serviceName,
			"operation", job.operation,
			"error", serviceActionErrorDetail(err))
		return
	}
	job.complete()
}
