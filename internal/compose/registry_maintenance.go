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
	"strconv"
	"strings"
	"time"
)

var (
	// ErrRegistryMaintenanceUnsupported reports a registry configuration that
	// Redlaunch cannot safely switch to read-only mode.
	ErrRegistryMaintenanceUnsupported = errors.New("registry maintenance is not supported for this configuration")
)

// RegistryMaintenanceTarget describes the managed registry state needed to
// enter and leave read-only maintenance.
type RegistryMaintenanceTarget struct {
	ContainerID string
	ConfigPath  string
	Config      []byte
	WasRunning  bool
}

// RegistryContainerTarget inspects the managed registry container and reads its
// active configuration. It fails closed for mounted configuration files,
// REGISTRY_* environment overrides, and unsupported layouts.
func (r CommandRunner) RegistryContainerTarget(ctx context.Context, container string) (RegistryMaintenanceTarget, error) {
	ctx = normalizeContext(ctx)
	container = strings.TrimSpace(container)
	if container == "" {
		return RegistryMaintenanceTarget{}, errors.New("registry container name is required")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	inspect := exec.CommandContext(ctx, binary, "inspect", container)
	inspect.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := runStructuredCommand(inspect, "inspect registry container", "")
	if err != nil {
		return RegistryMaintenanceTarget{}, errors.Join(ErrRegistryUnavailable, err)
	}
	type containerInspection struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Cmd []string `json:"Cmd"`
			Env []string `json:"Env"`
		} `json:"Config"`
		Mounts []struct {
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	var inspections []containerInspection
	if err := json.Unmarshal(bytes.TrimSpace(output), &inspections); err != nil {
		return RegistryMaintenanceTarget{}, fmt.Errorf("decode registry container inspection: %w", err)
	}
	if len(inspections) != 1 {
		return RegistryMaintenanceTarget{}, errors.New("registry container inspection returned an unexpected result")
	}
	details := inspections[0]
	if strings.TrimSpace(details.ID) == "" {
		return RegistryMaintenanceTarget{}, errors.New("registry container inspection returned no identifier")
	}
	for _, value := range details.Config.Env {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "REGISTRY_") {
			return RegistryMaintenanceTarget{}, fmt.Errorf("%w: registry environment overrides are not supported", ErrRegistryMaintenanceUnsupported)
		}
	}

	candidates := registryConfigCandidates(details.Config.Cmd)
	var configPath string
	var contents []byte
	for _, candidate := range candidates {
		read := exec.CommandContext(ctx, binary, "exec", container, "cat", "--", candidate)
		read.Env = composeProcessEnvironment(os.Environ(), nil)
		value, readErr := runStructuredCommand(read, "read registry configuration", "")
		if readErr == nil {
			configPath = candidate
			contents = value
			break
		}
		if !isContainerPathNotFound(readErr) {
			return RegistryMaintenanceTarget{}, readErr
		}
	}
	if configPath == "" {
		return RegistryMaintenanceTarget{}, fmt.Errorf("%w: registry configuration file was not found", ErrRegistryMaintenanceUnsupported)
	}
	for _, mount := range details.Mounts {
		destination := filepath.Clean(strings.TrimSpace(mount.Destination))
		if destination == "" {
			continue
		}
		if pathWithin(destination, configPath) {
			return RegistryMaintenanceTarget{}, fmt.Errorf("%w: registry configuration is mounted from the host", ErrRegistryMaintenanceUnsupported)
		}
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return RegistryMaintenanceTarget{}, fmt.Errorf("%w: registry configuration is empty", ErrRegistryMaintenanceUnsupported)
	}
	return RegistryMaintenanceTarget{
		ContainerID: details.ID,
		ConfigPath:  configPath,
		Config:      contents,
		WasRunning:  details.State.Running,
	}, nil
}

func registryConfigCandidates(command []string) []string {
	candidates := make([]string, 0, 3)
	for _, argument := range command {
		if validRegistryConfigPath(argument) {
			candidates = append(candidates, filepath.Clean(argument))
		}
	}
	for _, fallback := range []string{"/etc/distribution/config.yml", "/etc/docker/registry/config.yml"} {
		found := false
		for _, candidate := range candidates {
			if candidate == fallback {
				found = true
				break
			}
		}
		if !found {
			candidates = append(candidates, fallback)
		}
	}
	return candidates
}

func validRegistryConfigPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	if !strings.HasPrefix(value, "/etc/") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(value))
	return ext == ".yml" || ext == ".yaml"
}

func pathWithin(parent, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func isContainerPathNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file or directory") || strings.Contains(message, "could not find the file")
}

// RegistryReadOnlyConfig returns a copy of a registry configuration with
// storage.maintenance.readonly.enabled set to true. It preserves comments and
// formatting and refuses unexpected layouts rather than editing them.
func RegistryReadOnlyConfig(contents []byte) ([]byte, error) {
	lines := strings.Split(string(contents), "\n")
	storageIndex := -1
	storageIndent := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if trimmed == "storage:" {
			if indent != 0 {
				continue
			}
			if storageIndex >= 0 {
				return nil, fmt.Errorf("%w: duplicate storage section", ErrRegistryMaintenanceUnsupported)
			}
			storageIndex = index
			storageIndent = indent
		}
	}
	if storageIndex < 0 {
		return nil, fmt.Errorf("%w: registry storage section was not found", ErrRegistryMaintenanceUnsupported)
	}
	childIndent := -1
	for index := storageIndex + 1; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(lines[index])
		if indent <= storageIndent {
			break
		}
		childIndent = indent
		break
	}
	if childIndent <= storageIndent {
		return nil, fmt.Errorf("%w: registry storage section is empty", ErrRegistryMaintenanceUnsupported)
	}
	hasFilesystem := false
	for index := storageIndex + 1; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(lines[index])
		if indent <= storageIndent {
			break
		}
		if indent == childIndent && strings.HasPrefix(trimmed, "filesystem:") {
			hasFilesystem = true
		}
		if strings.HasPrefix(trimmed, "maintenance:") {
			return nil, fmt.Errorf("%w: registry maintenance section already exists", ErrRegistryMaintenanceUnsupported)
		}
	}
	if !hasFilesystem {
		return nil, fmt.Errorf("%w: registry filesystem storage is required", ErrRegistryMaintenanceUnsupported)
	}
	block := []string{
		strings.Repeat(" ", childIndent) + "maintenance:",
		strings.Repeat(" ", childIndent+4) + "readonly:",
		strings.Repeat(" ", childIndent+8) + "enabled: true",
	}
	result := make([]string, 0, len(lines)+len(block))
	result = append(result, lines[:storageIndex+1]...)
	result = append(result, block...)
	result = append(result, lines[storageIndex+1:]...)
	return []byte(strings.Join(result, "\n")), nil
}

func leadingSpaces(value string) int {
	return len(value) - len(strings.TrimLeft(value, " "))
}

// ApplyRegistryConfig atomically replaces the configuration inside the
// container. The target path must already be validated by
// RegistryContainerTarget.
func (r CommandRunner) ApplyRegistryConfig(ctx context.Context, container, configPath string, contents []byte) error {
	ctx = normalizeContext(ctx)
	if strings.TrimSpace(container) == "" {
		return errors.New("registry container name is required")
	}
	if !validRegistryConfigPath(configPath) {
		return errors.New("registry configuration path is invalid")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	staging, err := os.CreateTemp("", "redlaunch-registry-config-")
	if err != nil {
		return fmt.Errorf("create registry configuration staging file: %w", err)
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	if err := staging.Chmod(0o600); err != nil {
		staging.Close()
		return fmt.Errorf("protect registry configuration staging file: %w", err)
	}
	if _, err := staging.Write(contents); err != nil {
		staging.Close()
		return fmt.Errorf("write registry configuration staging file: %w", err)
	}
	if err := staging.Sync(); err != nil {
		staging.Close()
		return fmt.Errorf("sync registry configuration staging file: %w", err)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("close registry configuration staging file: %w", err)
	}

	containerStaging := configPath + ".redlaunch-new"
	copyCommand := exec.CommandContext(ctx, binary, "cp", stagingPath, container+":"+containerStaging)
	copyCommand.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(copyCommand, "stage registry configuration", ""); err != nil {
		return err
	}
	rename := exec.CommandContext(ctx, binary, "exec", container, "mv", "-f", "--", containerStaging, configPath)
	rename.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(rename, "activate registry configuration", ""); err != nil {
		cleanup := exec.CommandContext(ctx, binary, "exec", container, "rm", "-f", "--", containerStaging)
		cleanup.Env = composeProcessEnvironment(os.Environ(), nil)
		_ = runDiagnosticCommand(cleanup, "remove staged registry configuration", "")
		return err
	}
	return nil
}

// RestartRegistryContainer restarts the managed registry container. Docker
// terminates every process in the container before starting it again, which is
// the guarantee used to stop an uncertain garbage-collection process.
func (r CommandRunner) RestartRegistryContainer(ctx context.Context, container string) error {
	ctx = normalizeContext(ctx)
	container = strings.TrimSpace(container)
	if container == "" {
		return errors.New("registry container name is required")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, "restart", container)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(command, "restart registry container", ""); err != nil {
		return errors.Join(ErrRegistryUnavailable, err)
	}
	return nil
}

// WaitRegistryReady waits until the registry HTTP endpoint responds. It does not
// require a particular status code so an authenticated registry still reports
// as listening; write probes reject unsupported authentication separately.
func (r CommandRunner) WaitRegistryReady(ctx context.Context, container string, timeout time.Duration) error {
	ctx = normalizeContext(ctx)
	container = strings.TrimSpace(container)
	if container == "" {
		return errors.New("registry container name is required")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		status, err := r.registryHTTPStatus(ctx, container, "GET", "http://127.0.0.1:5000/v2/")
		if err == nil && status > 0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("wait for registry readiness: %w", err)
			}
			return errors.New("wait for registry readiness: registry did not respond")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RegistryWritesAccepted probes the registry write path. It returns true for a
// 2xx response and false for the read-only 405 response. Authentication or any
// other response is unsupported.
func (r CommandRunner) RegistryWritesAccepted(ctx context.Context, container, probeRepository string) (bool, error) {
	ctx = normalizeContext(ctx)
	if strings.TrimSpace(container) == "" {
		return false, errors.New("registry container name is required")
	}
	if _, err := applicationRepositoryProbe(probeRepository); err != nil {
		return false, err
	}
	url := "http://127.0.0.1:5000/v2/" + probeRepository + "/blobs/uploads/"
	status, err := r.registryHTTPStatus(ctx, container, "POST", url)
	if err != nil {
		return false, err
	}
	switch {
	case status == 405:
		return false, nil
	case status >= 200 && status < 300:
		return true, nil
	case status == 401 || status == 403:
		return false, fmt.Errorf("%w: registry authentication is not supported", ErrRegistryMaintenanceUnsupported)
	default:
		return false, fmt.Errorf("registry write probe returned HTTP %d", status)
	}
}

func applicationRepositoryProbe(repository string) (string, error) {
	if repository == "" || len(repository) > 128 {
		return "", errors.New("registry probe repository is invalid")
	}
	for _, part := range strings.Split(repository, "/") {
		if !validRegistryPathComponent(part) {
			return "", errors.New("registry probe repository is invalid")
		}
	}
	return repository, nil
}

// RemoveRegistryRepository removes the reserved probe repository after a
// writable verification.
func (r CommandRunner) RemoveRegistryRepository(ctx context.Context, container, repository string) error {
	ctx = normalizeContext(ctx)
	if _, err := applicationRepositoryProbe(repository); err != nil {
		return err
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	path := registryRepositoriesPath + "/" + repository
	command := exec.CommandContext(ctx, binary, "exec", container, "rm", "-rf", "--", path)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	return runDiagnosticCommand(command, "remove registry probe repository", "")
}

func (r CommandRunner) registryHTTPStatus(ctx context.Context, container, method, url string) (int, error) {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	args := []string{"exec", container, "wget", "-S", "-q", "-O", "/dev/null", "-T", "3"}
	if method == "POST" {
		args = append(args, "--post-data=")
	}
	args = append(args, url)
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	stdout := newTailBuffer(maxCommandOutput)
	stderr := newTailBuffer(maxCommandOutput)
	command.Stdout = stdout
	command.Stderr = stderr
	_ = command.Run()
	output := string(stderr.Bytes())
	if status := parseHTTPStatus(output); status > 0 {
		return status, nil
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return 0, errors.New("registry HTTP probe did not return a status")
}

func parseHTTPStatus(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "HTTP/") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status, err := strconv.Atoi(fields[1])
		if err == nil && status >= 100 && status <= 599 {
			return status
		}
	}
	return 0
}

// GarbageCollectRegistry runs the registry garbage collector inside the
// container. The caller must already have verified read-only mode.
func (r CommandRunner) GarbageCollectRegistry(ctx context.Context, container, configPath string) error {
	ctx = normalizeContext(ctx)
	if strings.TrimSpace(container) == "" {
		return errors.New("registry container name is required")
	}
	if !validRegistryConfigPath(configPath) {
		return errors.New("registry configuration path is invalid")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, "exec", container, "registry", "garbage-collect", "--quiet", configPath)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	return runDiagnosticCommand(command, "collect registry garbage", "")
}

// RegistryStorageBytes reports the apparent size of the registry data
// directory in bytes. The value is best effort and only used for reporting.
func (r CommandRunner) RegistryStorageBytes(ctx context.Context, container string) (int64, bool) {
	ctx = normalizeContext(ctx)
	if strings.TrimSpace(container) == "" {
		return 0, false
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	command := exec.CommandContext(ctx, binary, "exec", container, "du", "-sk", "/var/lib/registry")
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := runStructuredCommand(command, "measure registry storage", "")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, false
	}
	kilobytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || kilobytes < 0 {
		return 0, false
	}
	return kilobytes * 1024, true
}
