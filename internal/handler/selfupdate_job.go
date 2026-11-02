package handler

import (
	"sync"
	"time"

	"redlaunch/internal/application"
)

const (
	selfUpdateStateRunning  = "running"
	selfUpdateStateHandoff  = "handoff"
	selfUpdateStateComplete = "complete"
	selfUpdateStateFailed   = "failed"

	selfUpdateStepRemaining = "remaining"
	selfUpdateStepActive    = "active"
	selfUpdateStepComplete  = "complete"
	selfUpdateStepFailed    = "failed"

	selfUpdateJobRetention = time.Hour
)

type selfUpdateJobStore struct {
	mu   sync.Mutex
	jobs map[string]*selfUpdateJob
}

type selfUpdateJob struct {
	mu sync.RWMutex

	id           string
	state        string
	currentStage string
	errorStage   string
	errorDetail  string
	steps        []selfUpdateStepData
	finishedAt   time.Time
	blocked      bool
	revision     int
}

type selfUpdateStepData struct {
	Stage string
	Label string
	State string
}

type selfUpdateProgressData struct {
	JobID        string
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	StatusURL    string
	CloseURL     string
	Steps        []selfUpdateStepData
	UpdateState  *application.UpdateState
}

func newSelfUpdateJobStore() *selfUpdateJobStore {
	return &selfUpdateJobStore{jobs: make(map[string]*selfUpdateJob)}
}

func (s *selfUpdateJobStore) createUnique() (*selfUpdateJob, bool, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, false, err
	}
	job := &selfUpdateJob{
		id:    id,
		state: selfUpdateStateRunning,
		steps: selfUpdateSteps(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		state := existing.state
		finishedAt := existing.finishedAt
		blocked := existing.blocked
		existing.mu.RUnlock()
		if blocked || state == selfUpdateStateRunning || state == selfUpdateStateHandoff {
			return existing, false, nil
		}
		if !finishedAt.IsZero() && now.Sub(finishedAt) > selfUpdateJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, true, nil
}

func (s *selfUpdateJobStore) get(id string) *selfUpdateJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	return job
}

func (s *selfUpdateJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jobID, job := range s.jobs {
		job.mu.RLock()
		finishedAt := job.finishedAt
		blocked := job.blocked
		job.mu.RUnlock()
		if !blocked && !finishedAt.IsZero() && now.Sub(finishedAt) > selfUpdateJobRetention {
			delete(s.jobs, jobID)
		}
	}
}

func (j *selfUpdateJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != selfUpdateStateRunning {
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
		j.currentStage = selfUpdateStageLabel(stage)
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == selfUpdateStepRemaining || j.steps[index].State == selfUpdateStepActive {
			j.steps[index].State = selfUpdateStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = selfUpdateStepActive
}

func (j *selfUpdateJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != selfUpdateStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == selfUpdateStepRemaining || j.steps[index].State == selfUpdateStepActive {
			j.steps[index].State = selfUpdateStepComplete
		}
	}
	j.state = selfUpdateStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

// completeHandoff leaves restart active and pollable until durable state reports
// the outcome. The manager may remain running if the helper fails before
// recreation, so the durable outcome must also finalize this in-memory job.
func (j *selfUpdateJob) completeHandoff() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != selfUpdateStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].Stage == "restart" {
			j.steps[index].State = selfUpdateStepActive
			continue
		}
		if j.steps[index].State == selfUpdateStepRemaining || j.steps[index].State == selfUpdateStepActive {
			j.steps[index].State = selfUpdateStepComplete
		}
	}
	j.state = selfUpdateStateHandoff
	j.currentStage = "Restart Redlaunch"
}

// reconcile finishes only the matching handed-off job. An uncertain operation
// stays protected from retry/expiry until explicit acknowledgement; failed and
// completed jobs no longer prevent admission of a new operation. Ignore older
// snapshots so concurrent status and retry requests cannot undo acknowledgement.
func (s *selfUpdateJobStore) reconcile(state application.UpdateState) {
	job := s.get(state.OperationID)
	if job == nil || !state.Terminal() {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.state == selfUpdateStateRunning || state.Revision < job.revision {
		return
	}
	progress := persistedUpdateProgress(state)
	job.state = progress.State
	job.currentStage = progress.CurrentStage
	job.errorStage = progress.ErrorStage
	job.errorDetail = progress.ErrorDetail
	job.steps = progress.Steps
	job.blocked = state.Phase == application.UpdatePhaseUncertain && !state.Acknowledged
	job.revision = state.Revision
	if job.finishedAt.IsZero() {
		job.finishedAt = time.Now()
	}
}

func (j *selfUpdateJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != selfUpdateStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == selfUpdateStepActive {
			j.steps[index].State = selfUpdateStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Pull latest version"
	}
	j.state = selfUpdateStateFailed
	if err != nil {
		j.errorDetail = err.Error()
	} else {
		j.errorDetail = "The update could not be completed."
	}
	j.finishedAt = time.Now()
}

func (j *selfUpdateJob) snapshot() selfUpdateProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]selfUpdateStepData, len(j.steps))
	copy(steps, j.steps)
	return selfUpdateProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func selfUpdateStageLabel(stage string) string {
	labels := map[string]string{
		"check":    "Check GitHub Releases",
		"download": "Download release image",
		"verify":   "Verify release image",
		"load":     "Load release image",
		"install":  "Start update helper",
		"restart":  "Restart Redlaunch",
	}
	if label, ok := labels[stage]; ok {
		return label
	}
	return "Update Redlaunch"
}

func selfUpdateSteps() []selfUpdateStepData {
	return []selfUpdateStepData{
		{Stage: "check", Label: "Check GitHub Releases", State: selfUpdateStepRemaining},
		{Stage: "download", Label: "Download release image", State: selfUpdateStepRemaining},
		{Stage: "verify", Label: "Verify release image", State: selfUpdateStepRemaining},
		{Stage: "load", Label: "Load release image", State: selfUpdateStepRemaining},
		{Stage: "install", Label: "Start update helper", State: selfUpdateStepRemaining},
		{Stage: "restart", Label: "Restart Redlaunch", State: selfUpdateStepRemaining},
	}
}

// persistedUpdateProgress derives dialog steps from the durable installation
// state so progress survives the manager restart. Handoff (restart-queued and
// beyond) leaves restart active; only verified completion marks everything
// complete.
func persistedUpdateProgress(state application.UpdateState) selfUpdateProgressData {
	steps := selfUpdateSteps()
	completeThrough := -1
	active := -1
	failed := false
	uncertain := false
	complete := false
	switch state.Phase {
	case application.UpdatePhasePreparing:
		active = 0
	case application.UpdatePhaseDownloading:
		completeThrough, active = 0, 1
	case application.UpdatePhaseVerified:
		completeThrough, active = 1, 2
	case application.UpdatePhaseLoaded:
		completeThrough, active = 2, 3
	case application.UpdatePhaseRestartQueued,
		application.UpdatePhaseRecreating,
		application.UpdatePhaseVerifying:
		completeThrough, active = 4, 5
	case application.UpdatePhaseComplete:
		completeThrough = 5
		complete = true
	case application.UpdatePhaseFailed:
		completeThrough, active = 4, 5
		failed = true
	case application.UpdatePhaseUncertain:
		completeThrough, active = 4, 5
		uncertain = true
	}
	for index := range steps {
		switch {
		case (failed || uncertain) && index == active:
			steps[index].State = selfUpdateStepFailed
		case complete || index <= completeThrough:
			steps[index].State = selfUpdateStepComplete
		case index == active:
			steps[index].State = selfUpdateStepActive
		default:
			steps[index].State = selfUpdateStepRemaining
		}
	}
	progress := selfUpdateProgressData{
		JobID: string(state.Phase),
		Steps: steps,
	}
	if complete {
		progress.State = selfUpdateStateComplete
		progress.CurrentStage = "Complete"
	} else if failed || uncertain {
		progress.State = selfUpdateStateFailed
		progress.CurrentStage = steps[active].Label
		progress.ErrorStage = steps[active].Label
		progress.ErrorDetail = state.Error
		if uncertain && progress.ErrorDetail == "" {
			progress.ErrorDetail = "The update outcome could not be verified. Inspect the updater helper and the Redlaunch container before continuing."
		}
	} else if active >= 0 {
		progress.State = selfUpdateStateRunning
		progress.CurrentStage = steps[active].Label
	} else {
		progress.State = selfUpdateStateRunning
	}
	return progress
}
