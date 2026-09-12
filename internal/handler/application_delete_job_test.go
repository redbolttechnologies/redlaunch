package handler

import (
	"testing"

	"redlaunch/internal/application"
)

func TestApplicationDeleteProgressFromIntent(t *testing.T) {
	progress := applicationDeleteProgressFromIntent(application.ApplicationDeletionIntent{
		ApplicationID: 4,
		Name:          "demo",
		Stage:         "routing",
		State:         "failed",
	})

	if progress.State != applicationDeleteJobStateFailed || progress.CurrentStage != "Refresh application routing" {
		t.Fatalf("progress state = (%q, %q), want failed routing", progress.State, progress.CurrentStage)
	}
	if progress.ErrorDetail == "" || progress.ErrorStage != "Refresh application routing" {
		t.Fatalf("progress error = (%q, %q), want a safe routing checkpoint message", progress.ErrorStage, progress.ErrorDetail)
	}
	wantStates := []string{
		applicationDeleteJobStepComplete,
		applicationDeleteJobStepComplete,
		applicationDeleteJobStepComplete,
		applicationDeleteJobStepFailed,
		applicationDeleteJobStepRemaining,
		applicationDeleteJobStepRemaining,
	}
	if len(progress.Steps) != len(wantStates) {
		t.Fatalf("progress steps = %d, want %d", len(progress.Steps), len(wantStates))
	}
	for index, want := range wantStates {
		if progress.Steps[index].State != want {
			t.Fatalf("progress step %d state = %q, want %q", index, progress.Steps[index].State, want)
		}
	}
}

func TestApplicationDeleteProgressFromCompletedIntent(t *testing.T) {
	progress := applicationDeleteProgressFromIntent(application.ApplicationDeletionIntent{
		ApplicationID: 4,
		Name:          "demo",
		Stage:         "complete",
		State:         "complete",
	})

	if progress.State != applicationDeleteJobStateComplete || progress.CurrentStage != "Complete" {
		t.Fatalf("completed progress = (%q, %q), want complete", progress.State, progress.CurrentStage)
	}
	for index, step := range progress.Steps {
		if step.State != applicationDeleteJobStepComplete {
			t.Fatalf("completed progress step %d state = %q, want complete", index, step.State)
		}
	}
}
