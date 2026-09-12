package handler

import (
	"sync"
	"time"
)

const (
	selfUpdateStateRunning  = "running"
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
		existing.mu.RUnlock()
		if state == selfUpdateStateRunning {
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
		job.mu.RUnlock()
		if !finishedAt.IsZero() && now.Sub(finishedAt) > selfUpdateJobRetention {
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
		"pull":    "Pull latest version",
		"rebuild": "Rebuild container",
	}
	if label, ok := labels[stage]; ok {
		return label
	}
	return "Update Redlaunch"
}

func selfUpdateSteps() []selfUpdateStepData {
	return []selfUpdateStepData{
		{Stage: "pull", Label: "Pull latest version", State: selfUpdateStepRemaining},
		{Stage: "rebuild", Label: "Rebuild container", State: selfUpdateStepRemaining},
	}
}
