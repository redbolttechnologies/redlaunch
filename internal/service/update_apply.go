package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

// ApplyUpdateOptions is passed to the detached helper process.
type ApplyUpdateOptions struct {
	OperationID string
	StatePath   string
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// RunApplyUpdate performs the mutation phase inside the detached helper. It
// revalidates every persisted field before touching Docker.
func (s *UpdateService) RunApplyUpdate(ctx context.Context, options ApplyUpdateOptions) error {
	if s == nil || s.store == nil || s.docker == nil {
		return application.ErrUpdateUnavailable
	}
	state, ok, err := s.store.readState()
	if err != nil || !ok {
		return application.ErrUpdateUnavailable
	}
	if state.OperationID != strings.TrimSpace(options.OperationID) {
		return errors.New("update operation does not match the persisted state")
	}
	if strings.TrimSpace(options.StatePath) != "" && filepath.Clean(options.StatePath) != filepath.Clean(s.store.statePath) {
		return errors.New("update state path does not match the persisted state")
	}
	if state.Phase != application.UpdatePhaseRestartQueued {
		return errors.New("update operation is not ready for installation")
	}
	if state.TargetImageID == "" || state.PreviousImageID == "" || state.RuntimeImage == "" {
		return errors.New("update state is incomplete")
	}
	if strings.Contains(state.RuntimeImage, "@") {
		return errors.New("digest-pinned runtime images require a manual update")
	}
	if err := s.store.requireUpdateLock(state.OperationID); err != nil {
		return application.ErrUpdateBusy
	}
	if err := s.validateApplyState(state); err != nil {
		return s.uncertain(&state, "The persisted update state does not match this installation.")
	}
	target, err := s.docker.InspectImage(ctx, state.TargetImageID)
	if err != nil || target.ID != state.TargetImageID {
		return s.uncertain(&state, "The target release image is unavailable.")
	}
	if err := validateLoadedImage(target, application.ReleaseImage{ImageID: state.TargetImageID, Arch: s.arch}, state.Version, state.Commit, state.DeploymentContract); err != nil {
		return s.uncertain(&state, "The target release image no longer matches the release contract.")
	}
	current, err := s.docker.InspectContainer(ctx, state.ManagerContainer)
	if err != nil && !errors.Is(err, application.ErrNotFound) {
		return s.uncertain(&state, "The running Redlaunch container could not be inspected.")
	}
	if current.Image != "" && current.Image != state.PreviousImageID && current.Image != state.TargetImageID {
		return s.uncertain(&state, "The running Redlaunch image changed during installation.")
	}
	// Revalidate the live deployment immediately before mutation, after the
	// download, so a changed Compose file or mount cannot be applied silently.
	if err := s.validateDeployment(ctx, state, current); err != nil {
		return s.uncertain(&state, "The installation layout changed before installation.")
	}

	override := s.store.appOverridePath()
	if err := writeManagedFile(override, updateImageOverride(state.TargetImageID), 0o600); err != nil {
		return s.fail(&state, "The update image override could not be written.")
	}
	config, err := s.docker.ComposeConfig(ctx, state.InstallDir, state.ProjectName, state.ComposeFile, override)
	if err != nil {
		_ = os.Remove(override)
		return s.fail(&state, "The update Compose configuration is invalid.")
	}
	if err := validateUpdateComposeConfig(config, state.TargetImageID); err != nil {
		_ = os.Remove(override)
		return s.fail(&state, "The update Compose configuration is not supported.")
	}

	state.Phase = application.UpdatePhaseRecreating
	state.Owner = "helper"
	state.UpdatedAt = s.now().UTC()
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	// Recheck immediately before the first Docker mutation in case ownership
	// changed after the preflight validation.
	if err := s.store.requireUpdateLock(state.OperationID); err != nil {
		return application.ErrUpdateBusy
	}
	if err := s.docker.TagImage(ctx, state.TargetImageID, state.RuntimeImage); err != nil {
		// A Docker client error does not prove the daemon rejected the tag.
		// Retain the operation and lock for explicit recovery instead of
		// reporting a safe failure.
		return s.uncertain(&state, "The runtime image reference could not be confirmed.")
	}

	recreateCtx, cancel := context.WithTimeout(ctx, updateLoadTimeout)
	err = s.docker.RecreateApp(recreateCtx, state.InstallDir, state.ProjectName, state.ComposeFile, override)
	cancel()
	if err != nil {
		// Do not attempt an automatic rollback: the daemon may have partially
		// recreated the container. Keep the override and operation artifacts
		// for operator recovery and retain the installation lock.
		return s.uncertain(&state, "The Redlaunch container recreation did not complete.")
	}

	container, err := s.docker.InspectContainer(ctx, state.ManagerContainer)
	if err != nil {
		return s.uncertain(&state, "The recreated Redlaunch container could not be inspected.")
	}
	if container.Image != state.TargetImageID || !container.State.Running {
		return s.uncertain(&state, "The recreated Redlaunch container is not using the target image.")
	}
	state.Phase = application.UpdatePhaseVerifying
	state.UpdatedAt = s.now().UTC()
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	healthCtx, cancel := context.WithTimeout(ctx, updateHealthTimeout)
	err = s.verifyHealth(healthCtx, state, container)
	cancel()
	if err != nil {
		return s.uncertain(&state, "The updated Redlaunch instance did not become ready.")
	}
	if err := s.verifyRuntimeImage(ctx, state); err != nil {
		return s.uncertain(&state, "The runtime image reference does not resolve to the target image.")
	}
	updateStateComplete(&state, state.TargetImageID, state.Version, s.now())
	state.Acknowledged = false
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	_ = os.Remove(override)
	s.cleanupDownload(state)
	return s.store.release(state.OperationID)
}

// persistedIdentityMatches verifies that a stored operation still belongs to
// this installation before recovery or mutation.
func (s *UpdateService) persistedIdentityMatches(state application.UpdateState) bool {
	if state.InstallDir != "" && filepath.Clean(state.InstallDir) != filepath.Clean(s.directory) {
		return false
	}
	if state.ManagerContainer != "" && state.ManagerContainer != s.managerContainer {
		return false
	}
	if state.RuntimeImage != "" && state.RuntimeImage != s.runtimeImage {
		return false
	}
	return true
}

// validateApplyState guarantees the helper only mutates the deployment that
// the manager recorded. The state file is protected, but it is still persisted
// input and must not be trusted to redirect Compose or cleanup paths.
func (s *UpdateService) validateApplyState(state application.UpdateState) error {
	if filepath.Clean(state.InstallDir) != filepath.Clean(s.directory) {
		return errors.New("installation directory does not match the helper")
	}
	if state.ManagerContainer != s.managerContainer {
		return errors.New("manager container does not match the helper")
	}
	if state.RuntimeImage != s.runtimeImage {
		return errors.New("runtime image does not match the helper")
	}
	if state.DeploymentContract != s.currentContract {
		return errors.New("deployment contract does not match the helper")
	}
	if strings.TrimSpace(state.ProjectName) == "" {
		return errors.New("Compose project name is missing")
	}
	if _, err := application.ParseReleaseVersion(state.Version); err != nil {
		return errors.New("release version is invalid")
	}
	if strings.TrimSpace(state.Commit) == "" {
		return errors.New("release commit is missing")
	}
	composeFile, err := findUpdateComposeFile(s.directory)
	if err != nil {
		return err
	}
	if filepath.Clean(state.ComposeFile) != filepath.Clean(composeFile) {
		return errors.New("Compose file does not match the installation")
	}
	if !pathContained(state.InstallDir, state.ComposeFile) {
		return errors.New("Compose file is outside the installation directory")
	}
	if state.DownloadPath != "" && !pathContained(s.store.downloadDir(state.OperationID), state.DownloadPath) {
		return errors.New("download path is outside the update directory")
	}
	return nil
}

func (s *UpdateService) verifyHealth(ctx context.Context, state application.UpdateState, container compose.UpdateContainerInfo) error {
	endpoint, err := updateHealthEndpoint(container)
	if err != nil {
		return err
	}
	var lastErr error
	for {
		contents, callErr := s.docker.ExecHealth(ctx, state.ManagerContainer, endpoint)
		if callErr == nil {
			var health healthResponse
			if err := json.Unmarshal(contents, &health); err != nil {
				lastErr = err
			} else if health.Status == "ok" && health.Version == state.Version && health.Commit == state.Commit {
				return nil
			} else {
				lastErr = errors.New("health response does not match the release")
			}
		} else {
			lastErr = callErr
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *UpdateService) verifyRuntimeImage(ctx context.Context, state application.UpdateState) error {
	if state.RuntimeImage == "" || state.TargetImageID == "" {
		return errors.New("the runtime image reference is not configured")
	}
	image, err := s.docker.InspectImage(ctx, state.RuntimeImage)
	if err != nil || image.ID != state.TargetImageID {
		return errors.New("the runtime image reference does not resolve to the target image")
	}
	return nil
}

func updateImageOverride(imageID string) string {
	quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "$", "$$")) }
	return fmt.Sprintf("services:\n  app:\n    image: %s\n    pull_policy: never\n", quote(imageID))
}

func validateUpdateComposeConfig(contents []byte, expectedImageID string) error {
	var project struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal(contents, &project); err != nil {
		return err
	}
	app, ok := project.Services["app"]
	if !ok || strings.TrimSpace(app.Image) == "" {
		return errors.New("resolved app image is not the validated image ID")
	}
	if strings.TrimSpace(app.Image) != strings.TrimSpace(expectedImageID) {
		return errors.New("resolved app image is not the validated image ID")
	}
	return nil
}

func (s *UpdateService) writeHelperProject(state application.UpdateState) error {
	if s == nil || s.store == nil {
		return errors.New("update state store is not configured")
	}
	if err := ensureUpdatePrivateDirectory(s.store.helperDir()); err != nil {
		return err
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if err := writeManagedFile(filepath.Join(s.store.helperDir(), name), "", 0o600); err != nil {
			return err
		}
	}
	quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "$", "$$")) }
	host := os.Getenv("HOST_HOSTNAME")
	volumes := []string{
		"      - type: bind\n        source: " + quote(s.dockerSocket) + "\n        target: " + quote(s.dockerSocket),
		"      - type: bind\n        source: " + quote(state.InstallDir) + "\n        target: " + quote(state.InstallDir),
	}
	if !pathContained(state.InstallDir, s.projectsRoot) {
		volumes = append(volumes, "      - type: bind\n        source: "+quote(s.projectsRoot)+"\n        target: "+quote(s.projectsRoot))
	}
	contents := fmt.Sprintf(`name: %s
services:
  updater:
    image: %s
    pull_policy: never
    container_name: %s
    labels:
      redlaunch.managed: "true"
      redlaunch.updater: "true"
    env_file: [vars.env, secrets.env]
    restart: "no"
    entrypoint: ["/usr/local/bin/redlaunch", "apply-update"]
    command: ["--operation-id", %s, "--state", %s]
    working_dir: %s
    environment:
      PWD: %s
      HOSTNAME: %s
      HOST_HOSTNAME: %s
      PROJECTS_ROOT: %s
      REDLAUNCH_DIR: %s
      REDLAUNCH_IMAGE: %s
      REDLAUNCH_CONTAINER_NAME: %s
    volumes:
%s
`,
		updateHelperProjectName,
		quote(state.TargetImageID),
		updateHelperName,
		quote(state.OperationID),
		quote(s.store.statePath),
		quote(state.InstallDir),
		quote(state.InstallDir),
		quote(host),
		quote(host),
		quote(s.projectsRoot),
		quote(state.InstallDir),
		quote(s.runtimeImage),
		quote(s.managerContainer),
		strings.Join(volumes, "\n"),
	)
	return writeManagedFile(s.store.helperComposePath(), contents, 0o600)
}

func pathContained(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s *UpdateService) cleanupDownload(state application.UpdateState) {
	if strings.TrimSpace(state.DownloadPath) == "" {
		return
	}
	downloadDir := s.store.downloadDir(state.OperationID)
	if !pathContained(downloadDir, state.DownloadPath) ||
		filepath.Clean(state.DownloadPath) == filepath.Clean(downloadDir) {
		return
	}
	if err := validateUpdatePathBeforeCreate(s.projectsRoot, state.DownloadPath); err != nil {
		return
	}
	_ = os.Remove(state.DownloadPath)
	_ = os.Remove(filepath.Dir(state.DownloadPath))
	_ = os.Remove(s.store.appOverridePath())
}

func (s *UpdateService) fail(state *application.UpdateState, message string) error {
	state.Phase = application.UpdatePhaseFailed
	state.Owner = "helper"
	state.Error = message
	state.FinishedAt = s.now().UTC()
	state.UpdatedAt = s.now().UTC()
	if err := s.store.writeState(state); err != nil {
		// The transition was not persisted. Keep the lock and recovery
		// artifacts so startup reconciliation can decide the outcome instead
		// of releasing exclusion for a still-active operation.
		return errors.New(message)
	}
	s.cleanupDownload(*state)
	_ = s.store.release(state.OperationID)
	return errors.New(message)
}

func (s *UpdateService) uncertain(state *application.UpdateState, message string) error {
	state.Phase = application.UpdatePhaseUncertain
	state.Owner = "helper"
	state.Error = message
	state.UpdatedAt = s.now().UTC()
	_ = s.store.writeState(state)
	return application.ErrUpdateUncertain
}

// ReconcileUpdateState resolves persisted installation state at startup.
func (s *UpdateService) ReconcileUpdateState(ctx context.Context) error {
	if s == nil {
		return nil
	}
	err := s.reconcileUpdateState(ctx)
	if err == nil || errors.Is(err, application.ErrUpdateUncertain) || errors.Is(err, application.ErrUpdateBusy) {
		s.markAdmitted()
	}
	return err
}

// ReconcileInterruptedUpdate resolves a persisted operation that may have
// outlived its helper. Settings uses it before admission so a helper that died
// before recreating the manager cannot leave the installation in a permanent
// handoff or keep startup admission closed. ReconcileUpdateState checks the
// helper under its own transition rules; a live helper returns without
// changing the operation.
func (s *UpdateService) ReconcileInterruptedUpdate(ctx context.Context) error {
	if s == nil || s.store == nil || s.docker == nil {
		return application.ErrUpdateUnavailable
	}
	_, ok, err := s.store.readState()
	if err != nil || !ok {
		return err
	}
	return s.ReconcileUpdateState(ctx)
}

func (s *UpdateService) reconcileUpdateState(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	// A management-access restart owns the shared lock and has its own
	// reconciliation. Do not interpret its lock as an orphaned update lock.
	if managementRestartPending(s.projectsRoot) {
		return nil
	}
	state, ok, err := s.store.readState()
	if err != nil {
		return err
	}
	// Acknowledged terminal records are history. Later manual image or
	// container changes must not reopen them. Active and unacknowledged
	// operations still have to match this installation.
	if (!state.Terminal() || !state.Acknowledged) && !s.persistedIdentityMatches(state) {
		return s.reconcileUncertain(&state, "The persisted update state does not match this installation.")
	}
	if !ok {
		if s.store.operationRunning() {
			running, runningErr := s.helperRunning(ctx)
			if runningErr != nil {
				return runningErr
			}
			if !running {
				if err := s.store.removeOrphanedUpdateLock(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	running, err := s.helperRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	// Acknowledgement closes this operation. Later manual deployments are not
	// evidence that the historical operation needs to run again. Only finish
	// its own interrupted acknowledgement cleanup, never touch a newer lock.
	if state.Terminal() && state.Acknowledged {
		return s.finishAcknowledgedUpdate(state)
	}
	// Re-read before mutating: a new operation may have started while Docker
	// was being inspected. Aborting preserves the new operation's state and
	// lock instead of overwriting it or cleaning up its shared artifacts.
	stillCurrent := func(expected application.UpdateState) bool {
		current, ok, err := s.store.readState()
		if err != nil || !ok {
			return false
		}
		return current.OperationID == expected.OperationID && current.Revision == expected.Revision
	}
	if state.Terminal() {
		if state.Phase == application.UpdatePhaseComplete || state.Phase == application.UpdatePhaseUncertain {
			container, inspectErr := s.docker.InspectContainer(ctx, state.ManagerContainer)
			if inspectErr == nil && container.Image == state.TargetImageID && container.State.Running && s.verifyHealth(ctx, state, container) == nil {
				if err := s.verifyRuntimeImage(ctx, state); err != nil {
					return s.reconcileUncertain(&state, "The runtime image reference does not resolve to the target image.")
				}
				if !stillCurrent(state) {
					return application.ErrUpdateBusy
				}
				updateStateComplete(&state, state.TargetImageID, state.Version, s.now())
				if err := s.store.writeState(&state); err != nil {
					return err
				}
				s.cleanupDownload(state)
				return s.store.release(state.OperationID)
			}
			if state.Phase == application.UpdatePhaseComplete {
				if !stillCurrent(state) {
					return application.ErrUpdateBusy
				}
				return s.reconcileUncertain(&state, "The recorded completed update no longer matches the running container.")
			}
			// Uncertain outcomes retain the lock and recovery artifacts until an
			// operator positively verifies the installation or safely clears
			// it. Releasing here would permit another mutation while the Docker
			// outcome is still unknown.
			if state.Phase == application.UpdatePhaseUncertain {
				return application.ErrUpdateUncertain
			}
		}
		if !stillCurrent(state) {
			return application.ErrUpdateBusy
		}
		s.cleanupDownload(state)
		return s.store.release(state.OperationID)
	}
	container, inspectErr := s.docker.InspectContainer(ctx, state.ManagerContainer)
	if inspectErr == nil {
		if container.Image == state.TargetImageID && container.State.Running {
			if healthErr := s.verifyHealth(ctx, state, container); healthErr == nil {
				if err := s.verifyRuntimeImage(ctx, state); err != nil {
					return s.reconcileUncertain(&state, "The runtime image reference does not resolve to the target image.")
				}
				if !stillCurrent(state) {
					return application.ErrUpdateBusy
				}
				updateStateComplete(&state, state.TargetImageID, state.Version, s.now())
				if err := s.store.writeState(&state); err != nil {
					return err
				}
				s.cleanupDownload(state)
				return s.store.release(state.OperationID)
			}
		}
		if container.Image == state.PreviousImageID {
			postInstall := state.Phase == application.UpdatePhaseRestartQueued || state.Phase == application.UpdatePhaseRecreating || state.Phase == application.UpdatePhaseVerifying || state.Phase == application.UpdatePhaseUncertain
			if !stillCurrent(state) {
				return application.ErrUpdateBusy
			}
			if postInstall {
				// The helper may have tagged the new image, partially recreated
				// the container, or failed after either step. The previous image
				// being visible does not prove no mutation occurred, so retain
				// the lock and recovery artifacts for an operator.
				return s.reconcileUncertain(&state, "The update helper stopped after installation began; the running container still uses the previous image.")
			}
			state.Phase = application.UpdatePhaseFailed
			state.Owner = "manager"
			state.Error = "The update was interrupted before installation began."
			state.FinishedAt = s.now().UTC()
			if err := s.store.writeState(&state); err != nil {
				return err
			}
			s.cleanupDownload(state)
			return s.store.release(state.OperationID)
		}
	}
	if !stillCurrent(state) {
		return application.ErrUpdateBusy
	}
	return s.reconcileUncertain(&state, "The update was interrupted and its outcome could not be verified.")
}

// ClearUpdateState acknowledges a terminal or uncertain operation after an
// operator has inspected the installation. It never removes Docker resources.
func (s *UpdateService) ClearUpdateState(ctx context.Context, operationID string, confirmed bool) error {
	if s == nil || s.store == nil {
		return application.ErrUpdateUnavailable
	}
	if err := s.allowMutations(ctx); err != nil {
		return err
	}
	if !confirmed {
		return errors.New("explicit confirmation is required")
	}
	state, ok, err := s.store.readState()
	if err != nil || !ok {
		return errors.New("update state was not found")
	}
	if state.OperationID != strings.TrimSpace(operationID) {
		return errors.New("update operation does not match")
	}
	running, err := s.helperRunning(ctx)
	if err != nil {
		return errors.New("the update helper could not be inspected")
	}
	if running {
		return errors.New("the update helper is still running")
	}
	// Re-read after Docker inspection: the helper may have advanced the
	// operation while it was being inspected. Overwriting that progress and
	// releasing the lock would lose exclusion.
	current, ok, err := s.store.readState()
	if err != nil || !ok {
		return errors.New("update state was not found")
	}
	if current.OperationID != state.OperationID || current.Revision != state.Revision || current.Phase != state.Phase {
		return errors.New("update operation changed during inspection")
	}
	if !current.Terminal() {
		return errors.New("only terminal or uncertain updates can be acknowledged")
	}
	current.Acknowledged = true
	current.UpdatedAt = s.now().UTC()
	if err := s.store.writeState(&current); err != nil {
		return err
	}
	return s.finishAcknowledgedUpdate(current)
}

// finishAcknowledgedUpdate resumes acknowledgement after a crash between its
// durable write and lock release. Serialize the state/owner checks and cleanup
// so a historical acknowledgement cannot remove a newer operation's artifacts.
func (s *UpdateService) finishAcknowledgedUpdate(expected application.UpdateState) error {
	return s.store.withStoreLock(func() error {
		current, ok, err := s.store.readState()
		if err != nil {
			return err
		}
		if !ok || current.OperationID != expected.OperationID || current.Revision != expected.Revision || !current.Terminal() || !current.Acknowledged {
			return application.ErrUpdateBusy
		}
		operationID, owner, locked, err := s.store.readLock()
		if err != nil {
			return err
		}
		if locked && operationID != current.OperationID {
			return nil
		}
		if locked && owner != "manager" && owner != "helper" {
			return application.ErrUpdateBusy
		}
		s.cleanupDownload(current)
		if locked {
			if err := os.Remove(s.store.lockPath); err != nil {
				return err
			}
			return syncDirectory(s.store.root)
		}
		return nil
	})
}

func (s *UpdateService) reconcileUncertain(state *application.UpdateState, message string) error {
	if err := s.store.withStoreLock(func() error {
		current, ok, err := s.store.readState()
		if err != nil {
			return err
		}
		if !ok || current.OperationID != state.OperationID || current.Revision != state.Revision || current.Acknowledged {
			return application.ErrUpdateBusy
		}
		operationID, owner, locked, err := s.store.readLock()
		if err != nil {
			return err
		}
		if locked && (operationID != state.OperationID || (owner != "manager" && owner != "helper")) {
			return application.ErrUpdateBusy
		}
		if !locked {
			if err := s.store.createLock(state.OperationID, "manager"); err != nil {
				return err
			}
		}
		state.Phase = application.UpdatePhaseUncertain
		state.Owner = "manager"
		state.Error = message
		state.UpdatedAt = s.now().UTC()
		return s.store.writeStateLocked(state)
	}); err != nil {
		return err
	}
	return application.ErrUpdateUncertain
}

func (s *UpdateService) helperRunning(ctx context.Context) (bool, error) {
	exists, err := s.docker.ContainerExists(ctx, s.helperName)
	if err != nil || !exists {
		return false, err
	}
	container, err := s.docker.InspectContainer(ctx, s.helperName)
	if err != nil {
		return false, err
	}
	return container.State.Running, nil
}

// HelperRunning reports whether the detached update helper is running. It is
// used by HTTP guards that must fail closed when the helper cannot be
// inspected.
func (s *UpdateService) HelperRunning(ctx context.Context) (bool, error) {
	if s == nil || s.store == nil || s.docker == nil {
		return false, application.ErrUpdateUnavailable
	}
	return s.helperRunning(ctx)
}
