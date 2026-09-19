package handler

import (
	"errors"
	"strings"
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

func TestApplicationDeletionDiagnosticDetail(t *testing.T) {
	got := applicationDeletionDiagnosticDetail(errors.New("remove application resources: remove Compose project: exit status 1: Error: No such container: redbolt-7-web"))
	want := "remove Compose project: exit status 1: Error: No such container: redbolt-7-web"
	if got != want {
		t.Fatalf("diagnostic detail = %q, want %q", got, want)
	}
	if got := applicationDeletionDiagnosticDetail(nil); got != "" {
		t.Fatalf("diagnostic detail for nil error = %q, want empty", got)
	}
	redacted := applicationDeletionDiagnosticDetail(errors.New("remove application resources: something failed with token=s3cret-value"))
	if strings.Contains(redacted, "s3cret-value") {
		t.Fatalf("diagnostic detail leaked a secret: %q", redacted)
	}
	if !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("diagnostic detail did not redact: %q", redacted)
	}
	long := applicationDeletionDiagnosticDetail(errors.New("remove application resources: " + strings.Repeat("x", maxDeletionDiagnosticLength+10)))
	if len([]rune(long)) != maxDeletionDiagnosticLength+1 {
		t.Fatalf("diagnostic detail length = %d, want capped", len([]rune(long)))
	}
}

func TestServiceDeletionDiagnosticDetail(t *testing.T) {
	got := serviceDeletionDiagnosticDetail(errors.New("stop service: run compose stop: exit status 1: no such service: db"))
	if got != `run compose stop: exit status 1: no such service: db` {
		t.Fatalf("diagnostic detail = %q, want stripped prefixes", got)
	}
}
