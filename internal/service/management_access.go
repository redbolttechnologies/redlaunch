package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/config"
)

// SetMutationGate blocks access-mode changes until startup reconciliation has
// resolved any interrupted installation operation. It must be configured
// before the service starts handling requests.
func (s *SelfUpdateService) SetMutationGate(gate func(context.Context) error) {
	if s != nil {
		s.admit = gate
	}
}

// QueueManagementAccessMode edits only the startup mode and recreates only the
// manager. It shares the updater's lock and fixed helper name to exclude updates.
// Configuration contents and command output must never escape this service.
func (s *SelfUpdateService) QueueManagementAccessMode(ctx context.Context, mode string) error {
	if mode != config.AccessModeSSHOnly && mode != config.AccessModeManagedHTTPS {
		return errors.New("invalid management access mode")
	}
	if s.admit != nil {
		if err := s.admit(ctx); err != nil {
			return err
		}
	}
	if !s.mu.TryLock() {
		return errors.New("a Redlaunch installation change is already running")
	}
	defer s.mu.Unlock()
	if s.restartQueued {
		return errors.New("a Redlaunch access-mode restart is already queued")
	}
	// Acquire the shared installation lock before inspecting the helper so
	// updates, restarts, and pruning share atomic exclusion.
	store, err := newUpdateStateStore(s.projectsRoot)
	if err != nil {
		return errors.New("the installation state is unavailable")
	}
	operationID, err := newUpdateOperationID()
	if err != nil {
		return errors.New("the installation state is unavailable")
	}
	if err := store.acquire(operationID, "management-access"); err != nil {
		if errors.Is(err, application.ErrUpdateBusy) {
			return errors.New("a Redlaunch update is already running")
		}
		return errors.New("the installation state is unavailable")
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = store.release(operationID)
		}
	}()
	directory, err := s.validatedDirectory()
	if err != nil {
		return errors.New("the installation directory is unavailable")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return errors.New("the installation directory is unavailable")
	}
	running, err := s.helperRunning(ctx)
	if err != nil {
		return errors.New("the restart helper could not be inspected")
	}
	if running {
		return errors.New("a Redlaunch update or restart is already running")
	}
	if err := s.removeStoppedHelper(ctx); err != nil {
		return err
	}
	snapshot, err := snapshotManagedFile(filepath.Join(directory, ".env"))
	if err != nil || !snapshot.exists {
		return errors.New("a regular installation .env file is required")
	}
	if err := validateEnvironmentFileContents(snapshot.contents); err != nil {
		return errors.New("the installation .env must contain unique, single-line assignments; edit unsupported configuration manually")
	}
	const key = "MANAGEMENT_ACCESS_MODE"
	updated, err := replaceEnvironmentFile(string(snapshot.contents), key, key, mode)
	if errors.Is(err, application.ErrEnvironmentVariableNotFound) {
		updated, err = appendEnvironmentVariable(string(snapshot.contents), key, mode)
	}
	if err != nil {
		return errors.New("the management access mode could not be written")
	}
	if err := writeInstallationEnvironment(snapshot, updated, 0600); err != nil {
		if current, readErr := snapshotManagedFile(snapshot.path); readErr == nil && bytes.Equal(current.contents, []byte(updated)) {
			if restoreErr := writeInstallationEnvironment(snapshot, string(snapshot.contents), snapshot.mode); restoreErr != nil {
				return errors.New("configuration save failed and .env could not be restored; check it manually")
			}
		}
		return errors.New("the installation configuration could not be saved")
	}
	rollback := func(cause error) error {
		current, err := snapshotManagedFile(snapshot.path)
		if err != nil || !bytes.Equal(current.contents, []byte(updated)) {
			return errors.New("the restart failed and .env changed concurrently; check the installation configuration manually")
		}
		if err := writeInstallationEnvironment(snapshot, string(snapshot.contents), snapshot.mode); err != nil {
			return errors.New("the restart failed and the original configuration could not be restored; check .env manually")
		}
		return cause
	}
	// Resolve with Compose itself, not a substitute interpolation implementation.
	command := exec.CommandContext(ctx, s.dockerBinary, "compose", "--project-directory", directory, "--env-file", snapshot.path, "config", "--format", "json")
	command.Dir, command.Env = directory, managementComposeEnvironment(directory)
	output := newSelfUpdateTailBuffer(4 * 1024 * 1024)
	command.Stdout, command.Stderr = output, io.Discard
	if err := command.Run(); err != nil {
		return rollback(errors.New("Compose rejected the installation configuration"))
	}
	var resolved struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if json.Unmarshal(output.Bytes(), &resolved) != nil || resolved.Services["app"].Environment[key] != mode {
		return rollback(errors.New("the Compose app service must load MANAGEMENT_ACCESS_MODE from the installation .env"))
	}
	project, err := s.writeManagementRestartProject(directory, operationID)
	if err != nil {
		_ = s.clearManagementRestartMarker()
		return rollback(errors.New("the restart helper configuration could not be prepared"))
	}
	if err := s.writeManagementRestartMarker(managementRestartMarker{OperationID: operationID, Mode: mode, Phase: "queued"}); err != nil {
		_ = s.clearManagementRestartMarker()
		return rollback(errors.New("the restart helper state could not be prepared"))
	}
	command = exec.CommandContext(ctx, s.dockerBinary, "compose", "--project-directory", project, "--project-name", "redlaunch-manager-restart", "--env-file", filepath.Join(project, varsEnvFile), "-f", filepath.Join(project, "compose.yml"), "up", "-d", "--no-build")
	command.Dir, command.Env = project, managementComposeEnvironment(directory)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		// A client timeout may occur after Docker has already started the helper.
		inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		active, inspectErr := s.helperRunning(inspectCtx)
		cancel()
		if inspectErr != nil {
			// The helper may own the lock; retain it and the marker.
			lockHeld = false
			s.restartQueued = true
			return errors.New("configuration saved but restart status is unknown; inspect the Redlaunch helper before retrying")
		}
		if active {
			lockHeld = false
			s.restartQueued = true
			return nil
		}
		_ = s.clearManagementRestartMarker()
		return rollback(errors.New("the restart helper could not be started"))
	}
	// The detached helper now owns the shared lock and marker until it either
	// recreates the manager successfully or leaves an uncertain operation.
	lockHeld = false
	s.restartQueued = true
	return nil
}

// Preserve ownership so saving from the root container does not prevent the
// installation's host user from subsequently running Compose. Contents remain
// private throughout the atomic replacement.
func writeInstallationEnvironment(snapshot managedFileSnapshot, contents string, mode os.FileMode) error {
	info, err := os.Lstat(snapshot.path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("installation environment is not a regular file")
	}
	file, err := os.CreateTemp(filepath.Dir(snapshot.path), ".management-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.WriteString(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), snapshot.path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(snapshot.path))
}

func managementComposeEnvironment(directory string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PWD=" + directory}
	if host := os.Getenv("HOST_HOSTNAME"); host != "" {
		env = append(env, "HOSTNAME="+host)
	}
	return env
}

type managementRestartMarker struct {
	OperationID string `json:"operation_id"`
	Mode        string `json:"mode"`
	Phase       string `json:"phase"`
}

func (s *SelfUpdateService) managementRestartDir() string {
	return filepath.Join(s.projectsRoot, "core", "manager-restart")
}

func (s *SelfUpdateService) managementRestartMarkerPath() string {
	return filepath.Join(s.managementRestartDir(), "operation.json")
}

func (s *SelfUpdateService) writeManagementRestartMarker(marker managementRestartMarker) error {
	if !validUpdateOperationID(marker.OperationID) {
		return errors.New("restart operation is invalid")
	}
	if marker.Mode != config.AccessModeSSHOnly && marker.Mode != config.AccessModeManagedHTTPS {
		return errors.New("restart mode is invalid")
	}
	if marker.Phase != "queued" {
		return errors.New("restart phase is invalid")
	}
	if err := ensureDirectory(s.managementRestartDir()); err != nil {
		return err
	}
	contents, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return writeManagedFile(s.managementRestartMarkerPath(), string(contents)+"\n", 0o600)
}

func (s *SelfUpdateService) readManagementRestartMarker() (managementRestartMarker, bool, error) {
	path := s.managementRestartMarkerPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return managementRestartMarker{}, false, nil
	}
	if err != nil {
		return managementRestartMarker{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 8*1024 {
		return managementRestartMarker{}, false, errors.New("restart operation state is invalid")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return managementRestartMarker{}, false, err
	}
	var marker managementRestartMarker
	if err := json.Unmarshal(contents, &marker); err != nil {
		return managementRestartMarker{}, false, errors.New("restart operation state is invalid")
	}
	if !validUpdateOperationID(marker.OperationID) ||
		(marker.Mode != config.AccessModeSSHOnly && marker.Mode != config.AccessModeManagedHTTPS) ||
		marker.Phase != "queued" {
		return managementRestartMarker{}, false, errors.New("restart operation state is invalid")
	}
	return marker, true, nil
}

func (s *SelfUpdateService) clearManagementRestartMarker() error {
	if err := os.Remove(s.managementRestartMarkerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func managementRestartPending(projectsRoot string) bool {
	_, err := os.Lstat(filepath.Join(projectsRoot, "core", "manager-restart", "operation.json"))
	return err == nil
}

// ReconcileManagementRestart resolves a restart marker left by a detached
// helper. A running helper owns the operation. Once it exits, the process
// environment is authoritative: if this manager started with the requested
// mode the restart succeeded; otherwise the outcome is uncertain and the lock
// stays held for an operator to inspect.
func (s *SelfUpdateService) ReconcileManagementRestart(ctx context.Context) error {
	if s == nil {
		return nil
	}
	marker, ok, err := s.readManagementRestartMarker()
	if err != nil || !ok {
		return err
	}
	running, err := s.helperRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	store, err := newUpdateStateStore(s.projectsRoot)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MANAGEMENT_ACCESS_MODE")), marker.Mode) {
		if err := store.release(marker.OperationID); err != nil {
			return err
		}
		return s.clearManagementRestartMarker()
	}
	// The restart did not take effect. Preserve exclusion even if the helper
	// exited before releasing the lock.
	if !store.operationRunning() {
		if err := store.acquire(marker.OperationID, "manager"); err != nil {
			return err
		}
	}
	return application.ErrUpdateUncertain
}

func (s *SelfUpdateService) writeManagementRestartProject(directory, operationID string) (string, error) {
	root, err := filepath.Abs(s.projectsRoot)
	if err != nil || root == string(filepath.Separator) {
		return "", errors.New("invalid projects root")
	}
	core := filepath.Join(root, "core")
	project := filepath.Join(core, "manager-restart")
	for _, path := range []string{root, core, project} {
		if err := ensureDirectory(path); err != nil {
			return "", err
		}
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		path := filepath.Join(project, name)
		snapshot, err := snapshotManagedFile(path)
		if err != nil {
			return "", err
		}
		if snapshot.exists && len(snapshot.contents) != 0 {
			return "", errors.New("restart helper environment files must be empty")
		}
		if err := writeManagedFile(path, "", 0600); err != nil {
			return "", err
		}
	}
	// JSON strings are valid YAML scalars; server-configured paths are never
	// interpolated into a shell command. Both environment files stay empty.
	quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "$", "$$")) }
	volumes := []string{
		"      - type: bind\n        source: " + quote(s.dockerSocket) + "\n        target: " + quote(s.dockerSocket),
		"      - type: bind\n        source: " + quote(directory) + "\n        target: " + quote(directory),
	}
	// The helper reads its operation marker from PROJECTS_ROOT, which may be
	// configured outside the installation directory.
	if !pathContained(directory, root) {
		volumes = append(volumes, "      - type: bind\n        source: "+quote(root)+"\n        target: "+quote(root))
	}
	contents := fmt.Sprintf(`name: redlaunch-manager-restart
services:
  restart:
    image: %s
    pull_policy: never
    container_name: %s
    labels:
      redlaunch.managed: "true"
      redlaunch.updater: "true"
    env_file: [vars.env, secrets.env]
    restart: "no"
    entrypoint: ["redlaunch", "management-restart-run"]
    command: ["--directory", %s, "--projects-root", %s, "--operation-id", %s]
    working_dir: %s
    environment:
      PWD: %s
      HOST_HOSTNAME: %s
    volumes:
%s
`, quote(s.updaterImage), selfUpdateHelperName, quote(directory), quote(root), quote(operationID), quote(directory), quote(directory), quote(os.Getenv("HOST_HOSTNAME")), strings.Join(volumes, "\n"))
	path := filepath.Join(project, "compose.yml")
	if _, err := snapshotManagedFile(path); err != nil {
		return "", err
	}
	if err := writeManagedFile(path, contents, 0600); err != nil {
		return "", err
	}
	return project, nil
}

// removeStoppedHelper removes a stopped helper only when it carries the
// updater ownership labels. It never force-removes a running helper.
func (s *SelfUpdateService) removeStoppedHelper(ctx context.Context) error {
	command := exec.CommandContext(ctx, s.dockerBinary, "inspect", selfUpdateHelperName)
	command.Env = os.Environ()
	output := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		if isDockerNoSuchContainer(output.Bytes()) {
			return nil
		}
		return errors.New("the restart helper could not be inspected")
	}
	var rows []struct {
		Name  string `json:"Name"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(bytes.Clone(output.Bytes()), &rows); err != nil || len(rows) != 1 {
		return errors.New("the restart helper could not be inspected")
	}
	if rows[0].State.Running {
		return errors.New("a Redlaunch update or restart is already running")
	}
	labels := rows[0].Config.Labels
	if labels["redlaunch.managed"] != "true" || labels["redlaunch.updater"] != "true" {
		return errors.New("the restart helper name is owned by another container")
	}
	remove := exec.CommandContext(ctx, s.dockerBinary, "rm", selfUpdateHelperName)
	remove.Env = os.Environ()
	remove.Stdout, remove.Stderr = io.Discard, io.Discard
	if err := remove.Run(); err != nil {
		return errors.New("a Redlaunch update or restart is already running")
	}
	return nil
}

// RunManagementRestart runs in the detached helper, not the manager being
// replaced. The delay lets the browser receive the queued-restart response.
// The detached helper owns the shared installation lock until recreation
// succeeds; on failure the marker and lock remain for startup reconciliation.
func (s *SelfUpdateService) RunManagementRestart(ctx context.Context, operationID string) error {
	directory, err := s.validatedDirectory()
	if err != nil {
		return errors.New("the installation directory is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateManagementRestartOwnership(operationID); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	// Revalidate after the delay so a replaced marker or lock cannot authorize
	// this helper to mutate the installation.
	if err := s.validateManagementRestartOwnership(operationID); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, s.dockerBinary, "compose", "--project-directory", directory, "--env-file", filepath.Join(directory, ".env"), "up", "-d", "--no-build", "--pull", "never", "--no-deps", "--force-recreate", "app")
	command.Dir, command.Env = directory, managementComposeEnvironment(directory)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Redlaunch container recreation failed; check the installation with docker compose ps")
	}
	if err := s.validateManagementRestartOwnership(operationID); err != nil {
		return err
	}
	store, err := newUpdateStateStore(s.projectsRoot)
	if err != nil {
		return err
	}
	// Release before clearing the marker. If the helper exits between the two
	// steps, startup reconciliation still sees the marker and resolves the
	// lock. The reverse order would leave an unexplained lock behind.
	if err := store.release(operationID); err != nil {
		return err
	}
	return s.clearManagementRestartMarker()
}

// validateManagementRestartOwnership proves this helper still owns both the
// persisted restart marker and the shared installation lock before it is
// allowed to mutate Docker.
func (s *SelfUpdateService) validateManagementRestartOwnership(operationID string) error {
	marker, ok, err := s.readManagementRestartMarker()
	if err != nil {
		return err
	}
	if !ok || marker.OperationID != operationID {
		return errors.New("restart operation does not match the helper state")
	}
	store, err := newUpdateStateStore(s.projectsRoot)
	if err != nil {
		return errors.New("the installation state is unavailable")
	}
	lockOperation, owner, ok, err := store.readLock()
	if err != nil {
		return errors.New("the installation state is unavailable")
	}
	if !ok || lockOperation != operationID || owner != "management-access" {
		return errors.New("restart operation does not own the installation lock")
	}
	return nil
}
