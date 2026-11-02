package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type reconcileUpdateDocker struct {
	noopUpdateDocker
	containerImage string
	runtimeImage   string
	runtimeErr     error
	inspections    int
	healthCalls    int
	tagCalls       int
}

func (d *reconcileUpdateDocker) InspectContainer(context.Context, string) (compose.UpdateContainerInfo, error) {
	d.inspections++
	var container compose.UpdateContainerInfo
	container.Image = d.containerImage
	container.State.Running = true
	container.Config.Env = []string{"HTTP_ADDR=0.0.0.0:8080"}
	return container, nil
}

func (d *reconcileUpdateDocker) InspectImage(context.Context, string) (compose.UpdateImageInfo, error) {
	d.inspections++
	return compose.UpdateImageInfo{ID: d.runtimeImage}, d.runtimeErr
}

func (d *reconcileUpdateDocker) ExecHealth(context.Context, string, string) ([]byte, error) {
	d.healthCalls++
	return []byte(`{"status":"ok","version":"v1.2.3","commit":"abc123"}`), nil
}

func (d *reconcileUpdateDocker) TagImage(context.Context, string, string) error {
	d.tagCalls++
	return nil
}

func reconciliationState() application.UpdateState {
	return application.UpdateState{
		Schema:           updateStateSchema,
		OperationID:      "reconcile-operation",
		Owner:            "helper",
		Type:             updateOperationType,
		Phase:            application.UpdatePhaseVerifying,
		Version:          "v1.2.3",
		Commit:           "abc123",
		TargetImageID:    "sha256:target",
		PreviousImageID:  "sha256:previous",
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "redbolt-redlaunch",
	}
}

func writeReconciliationArtifacts(t *testing.T, updater *UpdateService, state *application.UpdateState) {
	t.Helper()
	state.DownloadPath = filepath.Join(updater.store.downloadDir(state.OperationID), "image.tar.gz")
	if err := os.MkdirAll(filepath.Dir(state.DownloadPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{state.DownloadPath, updater.store.appOverridePath()} {
		if err := os.WriteFile(path, []byte("test recovery artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReconcileVerifiesRuntimeReferenceBeforeCompletion(t *testing.T) {
	for _, phase := range []application.UpdateStatePhase{application.UpdatePhaseVerifying, application.UpdatePhaseUncertain, application.UpdatePhaseComplete} {
		for _, outcome := range []string{"target", "previous", "uninspectable"} {
			for _, locked := range []bool{false, true} {
				name := string(phase) + "/" + outcome + "/unlocked"
				if locked {
					name = string(phase) + "/" + outcome + "/locked"
				}
				t.Run(name, func(t *testing.T) {
					docker := &reconcileUpdateDocker{containerImage: "sha256:target", runtimeImage: "sha256:target"}
					if outcome == "previous" {
						docker.runtimeImage = "sha256:previous"
					}
					if outcome == "uninspectable" {
						docker.runtimeErr = errors.New("Docker inspection unavailable")
					}
					updater, err := NewUpdateService(UpdateOptions{ProjectsRoot: t.TempDir(), Directory: t.TempDir(), Docker: docker, DeferMutations: true})
					if err != nil {
						t.Fatal(err)
					}
					state := reconciliationState()
					state.Phase = phase
					writeReconciliationArtifacts(t, updater, &state)
					if err := updater.store.writeState(&state); err != nil {
						t.Fatal(err)
					}
					if locked {
						if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
							t.Fatal(err)
						}
					}
					err = updater.ReconcileUpdateState(t.Context())
					current, ok, readErr := updater.store.readState()
					if readErr != nil || !ok {
						t.Fatalf("state = %t, %v", ok, readErr)
					}
					if outcome == "target" {
						if err != nil || current.Phase != application.UpdatePhaseComplete || updater.UpdateInProgress() {
							t.Fatalf("verified recovery = %s, locked=%t, %v", current.Phase, updater.UpdateInProgress(), err)
						}
					} else {
						if !errors.Is(err, application.ErrUpdateUncertain) || current.Phase != application.UpdatePhaseUncertain {
							t.Fatalf("unverified recovery = %s, %v", current.Phase, err)
						}
						if err := updater.store.requireUpdateLock(state.OperationID); err != nil {
							t.Fatalf("recovery exclusion not retained: %v", err)
						}
					}
					for _, path := range []string{state.DownloadPath, updater.store.appOverridePath()} {
						_, statErr := os.Stat(path)
						if outcome == "target" && !errors.Is(statErr, os.ErrNotExist) {
							t.Fatalf("completed recovery retained artifact: %v", statErr)
						}
						if outcome != "target" && statErr != nil {
							t.Fatalf("uncertain recovery deleted artifact: %v", statErr)
						}
					}
					if docker.tagCalls != 0 || docker.healthCalls == 0 || docker.inspections < 2 {
						t.Fatalf("recovery must verify without retagging: %#v", docker)
					}
				})
			}
		}
	}
}

func TestAcknowledgedUpdateRemainsHistoryAfterManualDeployment(t *testing.T) {
	for _, phase := range []application.UpdateStatePhase{application.UpdatePhaseComplete, application.UpdatePhaseUncertain, application.UpdatePhaseFailed} {
		t.Run(string(phase), func(t *testing.T) {
			docker := &reconcileUpdateDocker{containerImage: "sha256:later-manual-deployment"}
			updater, err := NewUpdateService(UpdateOptions{ProjectsRoot: t.TempDir(), Directory: t.TempDir(), Docker: docker})
			if err != nil {
				t.Fatal(err)
			}
			state := reconciliationState()
			state.Phase = phase
			if err := updater.store.writeState(&state); err != nil {
				t.Fatal(err)
			}
			if err := updater.store.acquire(state.OperationID, "manager"); err != nil {
				t.Fatal(err)
			}
			if err := updater.ClearUpdateState(t.Context(), state.OperationID, true); err != nil {
				t.Fatal(err)
			}
			acknowledged, _, _ := updater.store.readState()
			for attempt := 0; attempt < 2; attempt++ {
				if err := updater.ReconcileUpdateState(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			current, _, _ := updater.store.readState()
			if !current.Acknowledged || current.Phase != phase || current.Revision != acknowledged.Revision || updater.UpdateInProgress() {
				t.Fatalf("acknowledged history reactivated: %#v", current)
			}
			if docker.inspections != 0 || docker.healthCalls != 0 || docker.tagCalls != 0 {
				t.Fatal("historical acknowledgement inspected or modified the later deployment")
			}
		})
	}
}

func TestAcknowledgedHistoryIgnoresChangedRuntimeIdentity(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     t.TempDir(),
		Directory:        t.TempDir(),
		RuntimeImage:     "redlaunch:new",
		ManagerContainer: "redbolt-redlaunch",
		Docker:           &noopUpdateDocker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := reconciliationState()
	state.RuntimeImage = "redlaunch:old"
	state.Phase = application.UpdatePhaseComplete
	state.Acknowledged = true
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.ReconcileUpdateState(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseComplete || !current.Acknowledged || current.Revision != state.Revision {
		t.Fatalf("acknowledged history changed: %#v, %t, %v", current, ok, err)
	}
}

func TestAcknowledgementCleanupResumesOnlyForItsOwnOperation(t *testing.T) {
	for _, lock := range []string{"own", "foreign", "none"} {
		t.Run(lock, func(t *testing.T) {
			updater, err := NewUpdateService(UpdateOptions{ProjectsRoot: t.TempDir(), Directory: t.TempDir(), Docker: &noopUpdateDocker{}})
			if err != nil {
				t.Fatal(err)
			}
			state := reconciliationState()
			state.Phase = application.UpdatePhaseUncertain
			state.Acknowledged = true
			writeReconciliationArtifacts(t, updater, &state)
			if err := updater.store.writeState(&state); err != nil {
				t.Fatal(err)
			}
			operationID := state.OperationID
			if lock == "foreign" {
				operationID = "later-operation"
			}
			if lock != "none" {
				if err := updater.store.acquire(operationID, "manager"); err != nil {
					t.Fatal(err)
				}
			}
			if err := updater.ReconcileUpdateState(t.Context()); err != nil {
				t.Fatal(err)
			}
			if lock == "foreign" {
				if err := updater.store.requireUpdateLock(operationID); err != nil {
					t.Fatalf("historical operation removed newer lock: %v", err)
				}
				if _, err := os.Stat(updater.store.appOverridePath()); err != nil {
					t.Fatalf("historical operation removed shared override: %v", err)
				}
			} else {
				if updater.UpdateInProgress() {
					t.Fatal("interrupted acknowledgement did not release its lock")
				}
				if _, err := os.Stat(updater.store.appOverridePath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("acknowledgement cleanup not resumed: %v", err)
				}
			}
		})
	}
}

func TestStaleRecoveryCannotReopenAcknowledgement(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{ProjectsRoot: t.TempDir(), Directory: t.TempDir(), Docker: &noopUpdateDocker{}})
	if err != nil {
		t.Fatal(err)
	}
	state := reconciliationState()
	state.Phase = application.UpdatePhaseComplete
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.ClearUpdateState(t.Context(), state.OperationID, true); err != nil {
		t.Fatal(err)
	}
	// Simulate reconciliation returning from Docker inspection with the
	// snapshot it read before the operator acknowledged the operation.
	if err := updater.reconcileUncertain(&state, "stale inspection"); !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("stale recovery = %v, want busy", err)
	}
	if updater.UpdateInProgress() {
		t.Fatal("stale recovery reacquired the acknowledged operation's lock")
	}
	current, _, _ := updater.store.readState()
	if !current.Acknowledged || current.Phase != application.UpdatePhaseComplete {
		t.Fatalf("stale recovery changed acknowledged history: %#v", current)
	}
}
