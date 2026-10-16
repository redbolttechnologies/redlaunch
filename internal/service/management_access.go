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

// QueueManagementAccessMode edits only the startup mode and recreates only the
// manager. It shares the updater's lock and fixed helper name to exclude updates.
// Configuration contents and command output must never escape this service.
func (s *SelfUpdateService) QueueManagementAccessMode(ctx context.Context, mode string) error {
	if mode != config.AccessModeSSHOnly && mode != config.AccessModeManagedHTTPS {
		return errors.New("invalid management access mode")
	}
	if !s.mu.TryLock() {
		return errors.New("a Redlaunch installation change is already running")
	}
	defer s.mu.Unlock()
	if s.restartQueued {
		return errors.New("a Redlaunch access-mode restart is already queued")
	}
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
	project, err := s.writeManagementRestartProject(directory)
	if err != nil {
		return rollback(errors.New("the restart helper configuration could not be prepared"))
	}
	remove := exec.CommandContext(ctx, s.dockerBinary, "rm", selfUpdateHelperName)
	remove.Stdout, remove.Stderr = io.Discard, io.Discard
	_ = remove.Run() // Never force-remove a running helper.
	command = exec.CommandContext(ctx, s.dockerBinary, "compose", "--project-directory", project, "--project-name", "redlaunch-manager-restart", "--env-file", filepath.Join(project, varsEnvFile), "-f", filepath.Join(project, "compose.yml"), "up", "-d", "--no-build")
	command.Dir, command.Env = project, managementComposeEnvironment(directory)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		// A client timeout may occur after Docker has already started the helper.
		inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		active, inspectErr := s.helperRunning(inspectCtx)
		cancel()
		if inspectErr != nil {
			s.restartQueued = true
			return errors.New("configuration saved but restart status is unknown; inspect the Redlaunch helper before retrying")
		}
		if active {
			s.restartQueued = true
			return nil
		}
		return rollback(errors.New("the restart helper could not be started"))
	}
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

func (s *SelfUpdateService) writeManagementRestartProject(directory string) (string, error) {
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
    command: ["--directory", %s]
    working_dir: %s
    environment:
      PWD: %s
      HOST_HOSTNAME: %s
    volumes:
      - type: bind
        source: %s
        target: %s
      - type: bind
        source: %s
        target: %s
`, quote(s.updaterImage), selfUpdateHelperName, quote(directory), quote(directory), quote(directory), quote(os.Getenv("HOST_HOSTNAME")), quote(s.dockerSocket), quote(s.dockerSocket), quote(directory), quote(directory))
	path := filepath.Join(project, "compose.yml")
	if _, err := snapshotManagedFile(path); err != nil {
		return "", err
	}
	if err := writeManagedFile(path, contents, 0600); err != nil {
		return "", err
	}
	return project, nil
}

// RunManagementRestart runs in the detached helper, not the manager being
// replaced. The delay lets the browser receive the queued-restart response.
func (s *SelfUpdateService) RunManagementRestart(ctx context.Context) error {
	directory, err := s.validatedDirectory()
	if err != nil {
		return errors.New("the installation directory is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	command := exec.CommandContext(ctx, s.dockerBinary, "compose", "--project-directory", directory, "--env-file", filepath.Join(directory, ".env"), "up", "-d", "--no-build", "--pull", "never", "--no-deps", "--force-recreate", "app")
	command.Dir, command.Env = directory, managementComposeEnvironment(directory)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Redlaunch container recreation failed; check the installation with docker compose ps")
	}
	return nil
}
