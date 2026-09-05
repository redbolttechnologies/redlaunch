package handler

import (
	"context"
	"sync"
	"time"
)

const (
	setupStateRunning  = "running"
	setupStateComplete = "complete"
	setupStateFailed   = "failed"

	setupStepRemaining = "remaining"
	setupStepActive    = "active"
	setupStepComplete  = "complete"
	setupStepFailed    = "failed"

	setupJobRetention = time.Hour
)

type progressSetupManager interface {
	SetupWithProgress(ctx context.Context, installProxy, installRegistry bool, progress func(stage, message string)) error
}

type setupJobStore struct {
	mu   sync.Mutex
	jobs map[string]*setupJob
}

type setupJob struct {
	mu sync.RWMutex

	id           string
	state        string
	currentStage string
	errorStage   string
	errorDetail  string
	steps        []setupStepData
	finishedAt   time.Time
}

type setupStepData struct {
	Stage string
	Label string
	State string
}

type setupProgressData struct {
	JobID        string
	State        string
	CurrentStage string
	ErrorStage   string
	ErrorDetail  string
	Steps        []setupStepData
}

func newSetupJobStore() *setupJobStore {
	return &setupJobStore{jobs: make(map[string]*setupJob)}
}

func (s *setupJobStore) create(installProxy, installRegistry bool) (*setupJob, error) {
	id, err := newCSRFToken()
	if err != nil {
		return nil, err
	}
	job := &setupJob{
		id:    id,
		state: setupStateRunning,
		steps: setupSteps(installProxy, installRegistry),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for jobID, existing := range s.jobs {
		existing.mu.RLock()
		finishedAt := existing.finishedAt
		existing.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > setupJobRetention {
			delete(s.jobs, jobID)
		}
	}
	s.jobs[id] = job
	return job, nil
}

func (s *setupJobStore) get(id string) *setupJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	return job
}

func (j *setupJob) update(stage, _ string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != setupStateRunning {
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
		j.currentStage = setupStageLabel(stage)
		return
	}
	for index := 0; index < stepIndex; index++ {
		if j.steps[index].State == setupStepRemaining || j.steps[index].State == setupStepActive {
			j.steps[index].State = setupStepComplete
		}
	}
	j.currentStage = j.steps[stepIndex].Label
	j.steps[stepIndex].State = setupStepActive
}

func (j *setupJob) complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != setupStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == setupStepRemaining || j.steps[index].State == setupStepActive {
			j.steps[index].State = setupStepComplete
		}
	}
	j.state = setupStateComplete
	j.currentStage = "Complete"
	j.finishedAt = time.Now()
}

func (j *setupJob) fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != setupStateRunning {
		return
	}
	for index := range j.steps {
		if j.steps[index].State == setupStepActive {
			j.steps[index].State = setupStepFailed
			j.errorStage = j.steps[index].Label
			break
		}
	}
	if j.errorStage == "" {
		j.errorStage = j.currentStage
	}
	if j.errorStage == "" {
		j.errorStage = "Prepare directories"
	}
	j.state = setupStateFailed
	j.errorDetail = err.Error()
	j.finishedAt = time.Now()
}

func (j *setupJob) snapshot() setupProgressData {
	j.mu.RLock()
	defer j.mu.RUnlock()
	steps := make([]setupStepData, len(j.steps))
	copy(steps, j.steps)
	return setupProgressData{
		JobID:        j.id,
		State:        j.state,
		CurrentStage: j.currentStage,
		ErrorStage:   j.errorStage,
		ErrorDetail:  j.errorDetail,
		Steps:        steps,
	}
}

func (h *Handler) runSetupJob(job *setupJob, installProxy, installRegistry bool) {
	var err error
	if manager, ok := h.setupManager.(progressSetupManager); ok {
		err = manager.SetupWithProgress(context.Background(), installProxy, installRegistry, job.update)
	} else {
		job.update("directories", "")
		err = h.setupManager.Setup(context.Background(), installProxy, installRegistry)
	}
	if err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("complete application setup", "stage", snapshot.ErrorStage, "error", err)
		return
	}
	job.complete()
}

func setupStageLabel(stage string) string {
	labels := map[string]string{
		"directories":    "Prepare directories",
		"proxy-files":    "Configure Caddy",
		"proxy-start":    "Start Caddy",
		"registry-files": "Configure Docker Registry",
		"registry-start": "Start Docker Registry",
		"finalize":       "Finalize setup",
		"setup":          "Install core services",
	}
	if label, ok := labels[stage]; ok {
		return label
	}
	return "Setup"
}

func setupSteps(installProxy, installRegistry bool) []setupStepData {
	steps := []setupStepData{{
		Stage: "directories",
		Label: "Prepare directories",
		State: setupStepRemaining,
	}}
	if installProxy {
		steps = append(steps,
			setupStepData{Stage: "proxy-files", Label: "Configure Caddy", State: setupStepRemaining},
			setupStepData{Stage: "proxy-start", Label: "Start Caddy", State: setupStepRemaining},
		)
	}
	if installRegistry {
		steps = append(steps,
			setupStepData{Stage: "registry-files", Label: "Configure Docker Registry", State: setupStepRemaining},
			setupStepData{Stage: "registry-start", Label: "Start Docker Registry", State: setupStepRemaining},
		)
	}
	return append(steps, setupStepData{
		Stage: "finalize",
		Label: "Finalize setup",
		State: setupStepRemaining,
	})
}
