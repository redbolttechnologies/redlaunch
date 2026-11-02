package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UpdateImageInfo is the subset of image metadata used by the release updater.
type UpdateImageInfo struct {
	ID           string            `json:"Id"`
	OS           string            `json:"Os"`
	Architecture string            `json:"Architecture"`
	Labels       map[string]string `json:"-"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// UpdateMountInfo is one host-visible container mount.
type UpdateMountInfo struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// UpdateContainerInfo is the subset of container metadata used by the updater.
type UpdateContainerInfo struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Config struct {
		Image      string            `json:"Image"`
		Hostname   string            `json:"Hostname"`
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	Mounts []UpdateMountInfo `json:"Mounts"`
}

// LoadImage imports a verified Docker image archive.
func (r CommandRunner) LoadImage(ctx context.Context, archive string) error {
	if strings.TrimSpace(archive) == "" {
		return errors.New("image archive is required")
	}
	return r.runUpdateCommand(ctx, "load image archive", "load", "-i", archive)
}

// InspectImage returns metadata for one image reference or ID.
func (r CommandRunner) InspectImage(ctx context.Context, reference string) (UpdateImageInfo, error) {
	if strings.TrimSpace(reference) == "" {
		return UpdateImageInfo{}, errors.New("image reference is required")
	}
	contents, err := r.runUpdateStructured(ctx, "inspect image", "image", "inspect", reference)
	if err != nil {
		return UpdateImageInfo{}, err
	}
	var rows []UpdateImageInfo
	if err := json.Unmarshal(bytes.TrimSpace(contents), &rows); err != nil || len(rows) != 1 {
		return UpdateImageInfo{}, errors.New("invalid image inspection response")
	}
	if rows[0].Labels == nil {
		rows[0].Labels = rows[0].Config.Labels
	}
	return rows[0], nil
}

// TagImage assigns target to the image identified by source.
func (r CommandRunner) TagImage(ctx context.Context, source, target string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		return errors.New("image tag source and target are required")
	}
	return r.runUpdateCommand(ctx, "tag image", "tag", source, target)
}

// ContainerExists reports whether a container with the exact name exists.
func (r CommandRunner) ContainerExists(ctx context.Context, name string) (bool, error) {
	if strings.TrimSpace(name) == "" {
		return false, errors.New("container name is required")
	}
	contents, err := r.runUpdateStructured(ctx, "find container", "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// RemoveContainer removes one stopped helper container. It never force-removes:
// callers must verify the helper is not running and carries the updater
// ownership labels before calling.
func (r CommandRunner) RemoveContainer(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("container name is required")
	}
	return r.runUpdateCommand(ctx, "remove update helper", "rm", name)
}

// InspectContainer returns metadata for one container name or ID.
func (r CommandRunner) InspectContainer(ctx context.Context, name string) (UpdateContainerInfo, error) {
	if strings.TrimSpace(name) == "" {
		return UpdateContainerInfo{}, errors.New("container name is required")
	}
	contents, err := r.runUpdateStructured(ctx, "inspect container", "inspect", name)
	if err != nil {
		return UpdateContainerInfo{}, err
	}
	var rows []UpdateContainerInfo
	if err := json.Unmarshal(bytes.TrimSpace(contents), &rows); err != nil || len(rows) != 1 {
		return UpdateContainerInfo{}, errors.New("invalid container inspection response")
	}
	return rows[0], nil
}

// ComposeConfig validates and resolves a Compose project with an optional
// image override file.
func (r CommandRunner) ComposeConfig(ctx context.Context, projectDir, projectName, composeFile, overrideFile string) ([]byte, error) {
	args := composeUpdateArgs(projectDir, projectName, composeFile, overrideFile, "config", "--format", "json")
	return r.runUpdateComposeStructured(ctx, projectDir, composeFile, "validate update Compose configuration", args...)
}

// RecreateApp recreates only the manager app service using the supplied
// resolved Compose files.
func (r CommandRunner) RecreateApp(ctx context.Context, projectDir, projectName, composeFile, overrideFile string) error {
	args := composeUpdateArgs(projectDir, projectName, composeFile, overrideFile,
		"up", "-d", "--no-build", "--pull", "never", "--no-deps", "--force-recreate", "app")
	return r.runUpdateComposeCommand(ctx, projectDir, composeFile, "recreate Redlaunch app", args...)
}

// StartUpdateHelper starts the detached updater Compose project.
func (r CommandRunner) StartUpdateHelper(ctx context.Context, projectDir, projectName string) error {
	args := []string{"compose", "--project-directory", projectDir, "--project-name", projectName,
		"--env-file", filepath.Join(projectDir, "vars.env"), "-f", filepath.Join(projectDir, "compose.yml"),
		"up", "-d", "--no-build"}
	return r.runUpdateCommand(ctx, "start Redlaunch update helper", args...)
}

// ExecHealth runs the image's local health client inside a container.
func (r CommandRunner) ExecHealth(ctx context.Context, container, endpoint string) ([]byte, error) {
	if strings.TrimSpace(container) == "" || strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("container and health endpoint are required")
	}
	return r.runUpdateStructured(ctx, "probe Redlaunch readiness",
		"exec", container, "/usr/local/bin/redlaunch", "healthcheck", "--url", endpoint)
}

func composeUpdateArgs(projectDir, projectName, composeFile, overrideFile string, command ...string) []string {
	args := []string{"compose", "--project-directory", projectDir}
	if strings.TrimSpace(projectName) != "" {
		args = append(args, "--project-name", projectName)
	}
	args = append(args, "-f", composeFile)
	if strings.TrimSpace(overrideFile) != "" {
		args = append(args, "-f", overrideFile)
	}
	return append(args, command...)
}

func (r CommandRunner) runUpdateComposeCommand(ctx context.Context, projectDir, composeFile, operation string, args ...string) error {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = projectDir
	command.Env = updateComposeEnvironment(projectDir, composeFile)
	output := newTailBuffer(maxCommandOutput)
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		details := strings.TrimSpace(string(output.Bytes()))
		if details == "" {
			return fmt.Errorf("%s: %w", operation, err)
		}
		return fmt.Errorf("%s: %w: %s", operation, err, details)
	}
	return nil
}

func (r CommandRunner) runUpdateCommand(ctx context.Context, operation string, args ...string) error {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	output := newTailBuffer(maxCommandOutput)
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		details := strings.TrimSpace(string(output.Bytes()))
		if details == "" {
			return fmt.Errorf("%s: %w", operation, err)
		}
		return fmt.Errorf("%s: %w: %s", operation, err, details)
	}
	return nil
}

func (r CommandRunner) runUpdateComposeStructured(ctx context.Context, projectDir, composeFile, operation string, args ...string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = projectDir
	command.Env = updateComposeEnvironment(projectDir, composeFile)
	output := newBoundedBuffer(maxStructuredOutput)
	command.Stdout = output
	command.Stderr = newTailBuffer(maxCommandOutput)
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if output.truncated {
		return nil, fmt.Errorf("%s: output exceeds limit", operation)
	}
	return bytes.Clone(output.Bytes()), nil
}

func updateComposeEnvironment(projectDir, composeFile string) []string {
	env := composeProcessEnvironment(os.Environ(), composeInterpolationVariables(projectDir, composeFile))
	for _, entry := range []string{
		"PWD=" + projectDir,
		"PROJECTS_ROOT=" + os.Getenv("PROJECTS_ROOT"),
		"REDLAUNCH_DIR=" + projectDir,
		"HOSTNAME=" + os.Getenv("HOST_HOSTNAME"),
		"HOST_HOSTNAME=" + os.Getenv("HOST_HOSTNAME"),
	} {
		key, _, _ := strings.Cut(entry, "=")
		filtered := env[:0]
		for _, candidate := range env {
			candidateKey, _, _ := strings.Cut(candidate, "=")
			if candidateKey != key {
				filtered = append(filtered, candidate)
			}
		}
		env = append(filtered, entry)
	}
	return env
}

func (r CommandRunner) runUpdateStructured(ctx context.Context, operation string, args ...string) ([]byte, error) {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	output := newBoundedBuffer(maxStructuredOutput)
	command.Stdout = output
	command.Stderr = newTailBuffer(maxCommandOutput)
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if output.truncated {
		return nil, fmt.Errorf("%s: output exceeds limit", operation)
	}
	return bytes.Clone(output.Bytes()), nil
}
