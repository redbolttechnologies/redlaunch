// Package compose runs Docker Compose projects.
package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxCommandOutput        = 8 * 1024
	maxStructuredOutput     = 4 * 1024 * 1024
	maxRecentLogOutput      = 1 * 1024 * 1024
	maxLogDownloadBytes     = 64 * 1024 * 1024
	maxConcurrentLogStreams = 2
)

// ErrLogOutputTooLarge means a log stream exceeded the maximum number of
// bytes Redlaunch will send or retain. The process is canceled as soon as the
// limit is observed so a noisy container cannot keep consuming resources.
var ErrLogOutputTooLarge = errors.New("log output exceeds the configured limit")

var managerEnvironmentKeys = map[string]struct{}{
	"AUTH_COOKIE_SECURE":      {},
	"AUTH_SESSION_SECRET":     {},
	"BACKUP_CONTAINER_NAME":   {},
	"BACKUP_DOCKER_BINARY":    {},
	"BACKUP_ROOT":             {},
	"DB_PATH":                 {},
	"DOTENV_FILE":             {},
	"GOOGLE_CLIENT_ID":        {},
	"GOOGLE_CLIENT_SECRET":    {},
	"GOOGLE_REDIRECT_URL":     {},
	"HTTP_ADDR":               {},
	"APP_BIND_ADDRESS":        {},
	"APP_PORT":                {},
	"MANAGEMENT_ACCESS_MODE":  {},
	"METRICS_FILESYSTEM_ROOT": {},
	"METRICS_PROC_ROOT":       {},
	"METRICS_SCOPE":           {},
	"PROJECTS_ROOT":           {},
	"SYSTEMD_BINARY":          {},
	"SYSTEMD_SCOPE":           {},
	"SYSTEMD_UNIT_DIR":        {},
}

var composeHostEnvironmentKeys = map[string]struct{}{
	"DOCKER_API_VERSION": {},
	"DOCKER_CERT_PATH":   {},
	"DOCKER_CONFIG":      {},
	"DOCKER_CONTEXT":     {},
	"DOCKER_HOST":        {},
	"DOCKER_TLS_VERIFY":  {},
	"HOME":               {},
	"PATH":               {},
	"TMPDIR":             {},
	"USER":               {},
}

// CommandRunner runs Docker Compose through the Docker CLI.
type CommandRunner struct {
	// Binary is the Docker CLI binary to execute. An empty value uses docker.
	Binary string
	// MaxLogBytes overrides the default per-download log limit. It is intended
	// for isolated integrations and tests; zero uses maxLogDownloadBytes.
	MaxLogBytes int64
}

// tailBuffer retains only the last max bytes written to it. os/exec writes to
// an io.Writer while the child is running, so bounding the writer is
// materially different from truncating CombinedOutput after the process has
// already finished.
type tailBuffer struct {
	max       int
	contents  []byte
	truncated bool
}

func newTailBuffer(max int) *tailBuffer {
	if max < 1 {
		max = 1
	}
	return &tailBuffer{max: max, contents: make([]byte, 0, max)}
}

func (b *tailBuffer) Write(contents []byte) (int, error) {
	if b == nil || len(contents) == 0 {
		return len(contents), nil
	}
	if len(contents) >= b.max {
		b.contents = b.contents[:b.max]
		copy(b.contents, contents[len(contents)-b.max:])
		b.truncated = true
		return len(contents), nil
	}
	if overflow := len(b.contents) + len(contents) - b.max; overflow > 0 {
		copy(b.contents, b.contents[overflow:])
		b.contents = b.contents[:len(b.contents)-overflow]
		b.truncated = true
	}
	b.contents = append(b.contents, contents...)
	return len(contents), nil
}

func (b *tailBuffer) Bytes() []byte {
	if b == nil {
		return nil
	}
	return b.contents
}

type boundedBuffer struct {
	max       int
	contents  []byte
	truncated bool
}

func newBoundedBuffer(max int) *boundedBuffer {
	if max < 1 {
		max = 1
	}
	return &boundedBuffer{max: max, contents: make([]byte, 0, max)}
}

func (b *boundedBuffer) Write(contents []byte) (int, error) {
	if b == nil || len(contents) == 0 {
		return len(contents), nil
	}
	remaining := b.max - len(b.contents)
	if remaining <= 0 {
		b.truncated = true
		return len(contents), nil
	}
	if len(contents) > remaining {
		b.contents = append(b.contents, contents[:remaining]...)
		b.truncated = true
		return len(contents), nil
	}
	b.contents = append(b.contents, contents...)
	return len(contents), nil
}

func (b *boundedBuffer) Bytes() []byte {
	if b == nil {
		return nil
	}
	return b.contents
}

var logStreamSlots = make(chan struct{}, maxConcurrentLogStreams)

type commandLogStream struct {
	stdout     io.ReadCloser
	command    *exec.Cmd
	ctx        context.Context
	cancel     context.CancelFunc
	release    func()
	stderr     *tailBuffer
	projectDir string
	maxBytes   int64
	readBytes  int64
	streamErr  error
	waitOnce   sync.Once
	waitErr    error
}

func (s *commandLogStream) Read(contents []byte) (int, error) {
	if s == nil || s.stdout == nil {
		return 0, errors.New("log stream is not configured")
	}
	if s.streamErr != nil {
		return 0, s.streamErr
	}
	if len(contents) == 0 {
		return 0, nil
	}

	if s.maxBytes > 0 && s.readBytes >= s.maxBytes {
		var probe [1]byte
		n, err := s.stdout.Read(probe[:])
		if n > 0 {
			s.cancel()
			s.streamErr = ErrLogOutputTooLarge
			return 0, s.streamErr
		}
		if err != nil {
			return 0, s.finish(err)
		}
	}

	if remaining := s.maxBytes - s.readBytes; remaining > 0 && int64(len(contents)) > remaining {
		contents = contents[:remaining]
	}
	n, err := s.stdout.Read(contents)
	s.readBytes += int64(n)
	if err != nil {
		return n, s.finish(err)
	}
	return n, nil
}

func (s *commandLogStream) finish(readErr error) error {
	waitErr := s.wait()
	if readErr != io.EOF {
		if waitErr != nil {
			return errors.Join(readErr, waitErr)
		}
		return readErr
	}
	if waitErr != nil {
		return waitErr
	}
	return io.EOF
}

func (s *commandLogStream) wait() error {
	s.waitOnce.Do(func() {
		waitErr := s.command.Wait()
		if s.ctx != nil && s.ctx.Err() != nil {
			s.waitErr = s.ctx.Err()
		} else {
			s.waitErr = waitErr
		}
		s.cancel()
		s.release()
	})
	if s.waitErr == nil {
		return nil
	}
	return composeCommandErrorForProject("read service logs", s.waitErr, s.stderr.Bytes(), s.projectDir)
}

func (s *commandLogStream) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	closeErr := s.stdout.Close()
	waitErr := s.wait()
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return errors.Join(closeErr, waitErr)
	}
	if s.streamErr != nil {
		return errors.Join(s.streamErr, waitErr)
	}
	return waitErr
}

func (r CommandRunner) logLimit() int64 {
	if r.MaxLogBytes > 0 {
		return r.MaxLogBytes
	}
	return maxLogDownloadBytes
}

func acquireLogStream(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case logStreamSlots <- struct{}{}:
		return func() { <-logStreamSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func runDiagnosticCommand(command *exec.Cmd, operation, projectDir string) error {
	stdout := newTailBuffer(maxCommandOutput)
	stderr := newTailBuffer(maxCommandOutput)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return composeCommandErrorForProject(operation, err, diagnosticOutput(stdout.Bytes(), stderr.Bytes()), projectDir)
	}
	return nil
}

func diagnosticOutput(stdout, stderr []byte) []byte {
	if len(stdout) == 0 {
		return stderr
	}
	if len(stderr) == 0 {
		return stdout
	}
	output := make([]byte, 0, len(stdout)+1+len(stderr))
	output = append(output, stdout...)
	output = append(output, '\n')
	output = append(output, stderr...)
	return output
}

func runStructuredCommand(command *exec.Cmd, operation, projectDir string) ([]byte, error) {
	stdout := newBoundedBuffer(maxStructuredOutput)
	stderr := newTailBuffer(maxCommandOutput)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, composeCommandErrorForProject(operation, err, stderr.Bytes(), projectDir)
	}
	if stdout.truncated {
		return nil, fmt.Errorf("%s: structured output exceeds %d bytes", operation, maxStructuredOutput)
	}
	return stdout.Bytes(), nil
}

// ServiceRuntime contains the Docker Compose runtime fields shown for a
// managed service. Ports and status intentionally retain Docker's display
// format so the UI matches docker ps output. Running is derived from Compose's
// machine-readable state for operational checks.
type ServiceRuntime struct {
	ServiceName   string
	ContainerName string
	CreatedAt     time.Time
	Status        string
	Image         string
	Ports         string
	Running       bool
}

// ConfiguredService contains the fields Redlaunch needs when importing a
// service from a Docker Compose project. The Compose definition remains the
// source of truth for the rest of the service configuration.
type ConfiguredService struct {
	Name  string
	Image string
}

// EnvironmentVariable is one resolved environment value from a Compose
// service definition.
type EnvironmentVariable struct {
	Key   string
	Value string
}

// Up starts all services in a Compose project in detached mode.
func (r CommandRunner) Up(ctx context.Context, projectDir string) error {
	return r.runComposeUp(ctx, projectDir, "")
}

// RestartProject rebuilds and force-recreates a Compose project in detached mode.
// This is used for managed gateways whose image embeds security-sensitive
// configuration and whose active connections must be dropped during key
// rotation or revocation.
func (r CommandRunner) RestartProject(ctx context.Context, projectDir string) error {
	return r.runComposeUpWithOptions(ctx, projectDir, "", "--build", "--force-recreate", "--wait", "--wait-timeout", "30")
}

// UpService starts one service in a Compose project in detached mode without
// starting unrelated services from the same project.
func (r CommandRunner) UpService(ctx context.Context, projectDir, serviceName string) error {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return errors.New("Compose service name is required")
	}
	return r.runComposeUp(ctx, projectDir, serviceName)
}

// EnsureNetwork creates the shared application network through the Docker
// adapter and verifies the ownership labels when it already exists. Compose
// projects consume this network as external infrastructure; optional core
// components do not implicitly own its lifetime.
func (r CommandRunner) EnsureNetwork(ctx context.Context, networkName string) error {
	ctx = normalizeContext(ctx)
	networkName = strings.TrimSpace(networkName)
	if !validDockerNetworkName(networkName) {
		return errors.New("Docker network name is invalid")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	list := exec.CommandContext(ctx, binary, "network", "ls", "--filter", "name=^"+regexp.QuoteMeta(networkName)+"$", "--format", "{{.Name}}")
	list.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := runStructuredCommand(list, "list Docker networks", "")
	if err != nil {
		return err
	}
	found := false
	for _, name := range strings.Fields(string(output)) {
		if name == networkName {
			found = true
			break
		}
	}
	if !found {
		create := exec.CommandContext(ctx, binary, "network", "create", "--driver", "bridge", "--label", "redlaunch.managed=true", "--label", "redlaunch.owner=redlaunch", networkName)
		create.Env = composeProcessEnvironment(os.Environ(), nil)
		if err := runDiagnosticCommand(create, "create application network", ""); err != nil {
			return err
		}
		return nil
	}

	inspect := exec.CommandContext(ctx, binary, "network", "inspect", "--format", "{{json .Labels}}", networkName)
	inspect.Env = composeProcessEnvironment(os.Environ(), nil)
	labelsOutput, err := runStructuredCommand(inspect, "inspect application network", "")
	if err != nil {
		return err
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(labelsOutput), &labels); err != nil {
		return fmt.Errorf("inspect application network: decode Docker labels: %w", err)
	}
	if labels["redlaunch.managed"] != "true" || labels["redlaunch.owner"] != "redlaunch" {
		return fmt.Errorf("refusing to use Docker network %q: it is not owned by Redlaunch", networkName)
	}
	return nil
}

func validDockerNetworkName(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && (character == '_' || character == '-' || character == '.') {
			continue
		}
		return false
	}
	return true
}

// ConfigServices validates a Compose project and returns its configured
// services without starting containers. Environment and filesystem
// interpolation are disabled because imports contain only the uploaded Compose
// file; the managed vars.env and secrets.env files are added by the service
// layer after validation.
func (r CommandRunner) ConfigServices(ctx context.Context, projectDir string) ([]ConfiguredService, error) {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return nil, fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "config", "--format", "json", "--no-interpolate", "--no-env-resolution", "--no-path-resolution")
	output, err := runStructuredCommand(command, "validate Compose project", projectDir)
	if err != nil {
		return nil, err
	}

	var config struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &config); err != nil {
		return nil, fmt.Errorf("decode Compose configuration: %w", err)
	}
	if len(config.Services) == 0 {
		return []ConfiguredService{}, nil
	}

	names := make([]string, 0, len(config.Services))
	for name := range config.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	services := make([]ConfiguredService, 0, len(names))
	for _, name := range names {
		services = append(services, ConfiguredService{
			Name:  name,
			Image: strings.TrimSpace(config.Services[name].Image),
		})
	}
	return services, nil
}

func (r CommandRunner) runComposeUp(ctx context.Context, projectDir, serviceName string) error {
	return r.runComposeUpWithOptions(ctx, projectDir, serviceName)
}

func (r CommandRunner) runComposeUpWithOptions(ctx context.Context, projectDir, serviceName string, options ...string) error {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return err
	}
	args := []string{"up", "-d"}
	args = append(args, options...)
	operation := "run compose project"
	if serviceName != "" {
		args = append(args, serviceName)
		operation = fmt.Sprintf("run compose service %q", serviceName)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, args...)
	if err := runDiagnosticCommand(command, operation, projectDir); err != nil {
		return err
	}
	return nil
}

// ReloadProxy asks the running Caddy container to load its mounted Caddyfile.
// The command is executed through Compose so the HTTP/service layer never
// needs to construct a Docker command itself.
func (r CommandRunner) ReloadProxy(ctx context.Context, projectDir string) error {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", "proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if err := runDiagnosticCommand(command, "reload Caddy proxy", projectDir); err != nil {
		return err
	}
	return nil
}

// Start starts one existing service in a Compose project.
func (r CommandRunner) Start(ctx context.Context, projectDir, serviceName string) error {
	return r.runServiceCommand(ctx, projectDir, "start", serviceName)
}

// Stop stops one service in a Compose project.
func (r CommandRunner) Stop(ctx context.Context, projectDir, serviceName string) error {
	return r.runServiceCommand(ctx, projectDir, "stop", serviceName)
}

// Restart restarts one service in a Compose project.
func (r CommandRunner) Restart(ctx context.Context, projectDir, serviceName string) error {
	return r.runServiceCommand(ctx, projectDir, "restart", serviceName)
}

// Remove removes one service's container from a Compose project. The service
// is stopped separately by the application service so this command cannot
// affect any other container in the project.
func (r CommandRunner) Remove(ctx context.Context, projectDir, serviceName string) error {
	return r.runServiceCommandWithOptions(ctx, projectDir, "rm", serviceName, "-f")
}

// Down stops and removes all containers and Compose-managed resources for a
// project. Volumes are included because they belong to the managed project;
// external networks and volumes remain untouched by Docker Compose.
func (r CommandRunner) Down(ctx context.Context, projectDir string) error {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return err
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "down", "--volumes", "--remove-orphans")
	if err := runDiagnosticCommand(command, "remove Compose project", projectDir); err != nil {
		return err
	}
	return nil
}

func (r CommandRunner) runServiceCommand(ctx context.Context, projectDir, action, serviceName string) error {
	return r.runServiceCommandWithOptions(ctx, projectDir, action, serviceName)
}

func (r CommandRunner) runServiceCommandWithOptions(ctx context.Context, projectDir, action, serviceName string, options ...string) error {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	if err := r.verifyProjectOwnership(ctx, projectDir); err != nil {
		return err
	}
	args := []string{action}
	args = append(args, options...)
	args = append(args, serviceName)
	command := composeCommand(ctx, binary, projectDir, composeFile, args...)
	if err := runDiagnosticCommand(command, fmt.Sprintf("run compose %s for service %q", action, serviceName), projectDir); err != nil {
		return err
	}
	return nil
}

// ListServices returns the current containers for a Compose project. The
// --all flag includes stopped containers, while the JSON format gives the
// caller the same status and ports strings Docker displays in docker ps.
func (r CommandRunner) ListServices(ctx context.Context, projectDir string) ([]ServiceRuntime, error) {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return nil, fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "ps", "-a", "--format", "json")
	output, err := runStructuredCommand(command, "list compose services", projectDir)
	if err != nil {
		return nil, err
	}
	return decodeServiceRuntimes(output)
}

// IsServiceRunning reports whether one Compose service currently has a
// running container. A missing service is treated as not running.
func (r CommandRunner) IsServiceRunning(ctx context.Context, projectDir, serviceName string) (bool, error) {
	services, err := r.ListServices(ctx, projectDir)
	if err != nil {
		return false, err
	}
	for _, service := range services {
		if service.ServiceName == serviceName {
			return service.Running, nil
		}
	}
	return false, nil
}

// Logs returns the most recent log lines for one Compose service. The caller
// chooses the tail size so the service layer can keep the dashboard bounded.
func (r CommandRunner) Logs(ctx context.Context, projectDir, serviceName string, tail int) (string, error) {
	if tail < 1 {
		return "", errors.New("log tail must be positive")
	}
	return r.readLogs(ctx, projectDir, serviceName, strconv.Itoa(tail))
}

// AllLogs returns the log history for one Compose service up to the configured
// download limit. HTTP downloads should use OpenLogs so the caller can apply
// backpressure instead of buffering this compatibility result in memory.
func (r CommandRunner) AllLogs(ctx context.Context, projectDir, serviceName string) (string, error) {
	stream, err := r.OpenLogs(ctx, projectDir, serviceName)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	contents, readErr := io.ReadAll(stream)
	if readErr != nil {
		return "", readErr
	}
	return string(contents), nil
}

func (r CommandRunner) readLogs(ctx context.Context, projectDir, serviceName, tail string) (string, error) {

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return "", fmt.Errorf("find Compose file: %w", err)
	}
	args := []string{"logs"}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	args = append(args, "--no-color", "--no-log-prefix", serviceName)
	command := composeCommand(ctx, binary, projectDir, composeFile, args...)
	output := newTailBuffer(maxRecentLogOutput)
	stderr := newTailBuffer(maxCommandOutput)
	command.Stdout = output
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", composeCommandErrorForProject("read service logs", err, stderr.Bytes(), projectDir)
	}
	return string(output.Bytes()), nil
}

// OpenLogs starts a backpressured Compose log process. The returned reader
// owns the process and must be closed by the caller. Closing it cancels the
// process, waits for exit, and releases the global log-stream slot.
func (r CommandRunner) OpenLogs(ctx context.Context, projectDir, serviceName string) (io.ReadCloser, error) {
	ctx = normalizeContext(ctx)
	release, err := acquireLogStream(ctx)
	if err != nil {
		return nil, err
	}

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		release()
		return nil, fmt.Errorf("find Compose file: %w", err)
	}
	commandContext, cancel := context.WithCancel(ctx)
	command := composeCommand(commandContext, binary, projectDir, composeFile, "logs", "--no-color", "--no-log-prefix", serviceName)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("open service log stream: %w", err)
	}
	stderr := newTailBuffer(maxCommandOutput)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		release()
		return nil, composeCommandErrorForProject("start service log stream", err, stderr.Bytes(), projectDir)
	}
	return &commandLogStream{
		stdout:     stdout,
		command:    command,
		ctx:        ctx,
		cancel:     cancel,
		release:    release,
		stderr:     stderr,
		projectDir: projectDir,
		maxBytes:   r.logLimit(),
	}, nil
}

const (
	postgresDumpScript    = `PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump --clean --if-exists --username "$POSTGRES_USER" --dbname "$POSTGRES_DB"`
	postgresRestoreScript = `PGPASSWORD="$POSTGRES_PASSWORD" exec psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB"`
)

// BackupPostgreSQL writes a plain SQL dump produced inside the PostgreSQL
// service container. The command only references environment variables that
// are already provided to the managed container, so credentials never pass
// through the host command line or HTTP layer.
func (r CommandRunner) BackupPostgreSQL(ctx context.Context, projectDir, serviceName, destination string) error {
	output, err := openManagedOutput(destination)
	if err != nil {
		return fmt.Errorf("open PostgreSQL backup: %w", err)
	}
	defer output.Close()

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", serviceName, "sh", "-c", postgresDumpScript)
	command.Stdout = output
	stderr := newTailBuffer(maxCommandOutput)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return composeCommandErrorForProject("create PostgreSQL backup", err, stderr.Bytes(), projectDir)
	}
	return nil
}

// RestorePostgreSQL restores a plain SQL dump through psql inside the
// PostgreSQL service container.
func (r CommandRunner) RestorePostgreSQL(ctx context.Context, projectDir, serviceName, source string) error {
	input, err := openManagedInput(source)
	if err != nil {
		return fmt.Errorf("open PostgreSQL backup: %w", err)
	}
	defer input.Close()

	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "exec", "-T", serviceName, "sh", "-c", postgresRestoreScript)
	command.Stdin = input
	stderr := newTailBuffer(maxCommandOutput)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return composeCommandErrorForProject("restore PostgreSQL backup", err, stderr.Bytes(), projectDir)
	}
	return nil
}

func openManagedOutput(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("backup destination must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

func openManagedInput(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("backup source must be a regular file")
	}
	return os.Open(path)
}

// Environment returns the resolved environment values for one Compose
// service. Compose config is used instead of reading environment files
// directly so the result follows Compose interpolation and env_file semantics.
func (r CommandRunner) Environment(ctx context.Context, projectDir, serviceName string) ([]EnvironmentVariable, error) {
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	composeFile, err := findComposeFile(projectDir)
	if err != nil {
		return nil, fmt.Errorf("find Compose file: %w", err)
	}
	command := composeCommand(ctx, binary, projectDir, composeFile, "config", "--format", "json", serviceName)
	output, err := runStructuredCommand(command, "read service environment", projectDir)
	if err != nil {
		return nil, err
	}
	return decodeServiceEnvironment(output, serviceName)
}

type composeConfig struct {
	Services map[string]composeConfigService `json:"services"`
}

type composeConfigService struct {
	Environment json.RawMessage `json:"environment"`
}

func decodeServiceEnvironment(output []byte, serviceName string) ([]EnvironmentVariable, error) {
	var config composeConfig
	if err := json.Unmarshal(bytes.TrimSpace(output), &config); err != nil {
		return nil, fmt.Errorf("decode Compose configuration: %w", err)
	}
	service, ok := config.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q is not present in Compose configuration", serviceName)
	}

	rawEnvironment := bytes.TrimSpace(service.Environment)
	if len(rawEnvironment) == 0 || bytes.Equal(rawEnvironment, []byte("null")) {
		return []EnvironmentVariable{}, nil
	}

	if rawEnvironment[0] == '{' {
		return decodeEnvironmentObject(rawEnvironment)
	}
	if rawEnvironment[0] == '[' {
		return decodeEnvironmentList(rawEnvironment)
	}
	return nil, errors.New("Compose service environment has an unsupported format")
}

func decodeEnvironmentObject(rawEnvironment []byte) ([]EnvironmentVariable, error) {
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal(rawEnvironment, &values); err != nil {
		return nil, fmt.Errorf("decode Compose service environment: %w", err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]EnvironmentVariable, 0, len(keys))
	for _, key := range keys {
		value, err := decodeEnvironmentValue(values[key])
		if err != nil {
			return nil, fmt.Errorf("decode environment value for %q: %w", key, err)
		}
		result = append(result, EnvironmentVariable{Key: key, Value: value})
	}
	return result, nil
}

func decodeEnvironmentList(rawEnvironment []byte) ([]EnvironmentVariable, error) {
	var entries []string
	if err := json.Unmarshal(rawEnvironment, &entries); err != nil {
		return nil, fmt.Errorf("decode Compose service environment: %w", err)
	}
	result := make([]EnvironmentVariable, 0, len(entries))
	for _, entry := range entries {
		key, value, hasValue := strings.Cut(entry, "=")
		if !hasValue {
			value = ""
		}
		result = append(result, EnvironmentVariable{Key: key, Value: value})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Key < result[right].Key
	})
	return result, nil
}

func decodeEnvironmentValue(rawValue json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(rawValue, &value); err == nil {
		return value, nil
	}
	var scalar any
	if err := json.Unmarshal(rawValue, &scalar); err != nil {
		return "", err
	}
	return fmt.Sprint(scalar), nil
}

var sensitiveDiagnosticAssignment = regexp.MustCompile(`(?i)(["']?(password|secret|token|api[_-]?key|private[_-]?key|credential)["']?[[:space:]]*[=:][[:space:]]*)("[^"\r\n]*"|'[^'\r\n]*'|[^[:space:],;}\]]+)`)

func composeCommandError(operation string, err error, output []byte) error {
	return composeCommandErrorForProject(operation, err, output, "")
}

func composeCommandErrorForProject(operation string, err error, output []byte, projectDir string) error {
	details := redactDiagnostic(string(output), projectDir)
	if len(details) > maxCommandOutput {
		details = "..." + details[len(details)-maxCommandOutput:]
	}
	if details != "" {
		return fmt.Errorf("%s: %w: %s", operation, err, details)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func redactDiagnostic(output, projectDir string) string {
	details := strings.TrimSpace(output)
	for _, secret := range diagnosticSecretValues(projectDir) {
		if secret != "" {
			details = strings.ReplaceAll(details, secret, "[REDACTED]")
		}
	}
	details = sensitiveDiagnosticAssignment.ReplaceAllString(details, `${1}[REDACTED]`)
	return details
}

func diagnosticSecretValues(projectDir string) []string {
	values := make([]string, 0)
	if projectDir != "" {
		if contents, err := os.ReadFile(filepath.Join(projectDir, "secrets.env")); err == nil {
			for _, line := range strings.Split(string(contents), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				_, value, ok := strings.Cut(trimmed, "=")
				if !ok {
					continue
				}
				value = strings.TrimSpace(value)
				if len(value) >= 3 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
					value = value[1 : len(value)-1]
				}
				if value != "" {
					values = append(values, value)
				}
			}
		}
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && sensitiveDiagnosticKey(key) && value != "" {
			values = append(values, value)
		}
	}
	return values
}

func sensitiveDiagnosticKey(key string) bool {
	key = strings.ToUpper(key)
	for _, part := range []string{"PASSWORD", "SECRET", "TOKEN", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

type composeServiceRuntime struct {
	Service   string `json:"Service"`
	Name      string `json:"Name"`
	Names     string `json:"Names"`
	CreatedAt string `json:"CreatedAt"`
	State     string `json:"State"`
	Status    string `json:"Status"`
	Image     string `json:"Image"`
	Ports     string `json:"Ports"`
}

func decodeServiceRuntimes(output []byte) ([]ServiceRuntime, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}

	var rows []composeServiceRuntime
	if output[0] == '[' {
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, fmt.Errorf("decode Compose service list: %w", err)
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var row composeServiceRuntime
			if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("decode Compose service: %w", err)
			}
			rows = append(rows, row)
		}
	}

	services := make([]ServiceRuntime, 0, len(rows))
	for _, row := range rows {
		containerName := strings.TrimSpace(row.Name)
		if containerName == "" {
			containerName = strings.TrimSpace(row.Names)
		}
		status := strings.TrimSpace(row.Status)
		if status == "" {
			status = strings.TrimSpace(row.State)
		}
		running := strings.EqualFold(strings.TrimSpace(row.State), "running")
		if strings.TrimSpace(row.State) == "" {
			lowerStatus := strings.ToLower(status)
			running = strings.HasPrefix(lowerStatus, "up") || strings.HasPrefix(lowerStatus, "running") || strings.HasPrefix(lowerStatus, "started")
		}
		services = append(services, ServiceRuntime{
			ServiceName:   strings.TrimSpace(row.Service),
			ContainerName: containerName,
			CreatedAt:     parseDockerCreatedAt(row.CreatedAt),
			Status:        status,
			Image:         strings.TrimSpace(row.Image),
			Ports:         strings.TrimSpace(row.Ports),
			Running:       running,
		})
	}
	return services, nil
}

func parseDockerCreatedAt(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05 -0700 MST",
		time.RFC3339Nano,
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func findComposeFile(projectDir string) (string, error) {
	for _, name := range []string{"compose.yml", "compose.yaml"} {
		path := filepath.Join(projectDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", name)
		}
		return name, nil
	}
	return "", errors.New("no compose.yaml or compose.yml file found")
}

// composeCommand applies the same explicit project identity and process
// environment policy to every Docker Compose operation. The project-root path
// scopes otherwise identical application trees installed on the same host,
// while the resource kind keeps core and application directories distinct.
func composeCommand(ctx context.Context, binary, projectDir, composeFile string, args ...string) *exec.Cmd {
	ctx = normalizeContext(ctx)
	composeArgs := []string{"compose", "--project-name", composeProjectName(projectDir), "--env-file", "/dev/null", "-f", composeFile}
	composeArgs = append(composeArgs, args...)
	command := exec.CommandContext(ctx, binary, composeArgs...)
	command.Dir = projectDir
	command.Env = composeProcessEnvironment(os.Environ(), composeInterpolationVariables(projectDir, composeFile))
	return command
}

func composeProjectName(projectDir string) string {
	clean := filepath.Clean(projectDir)
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}
	resourceName := filepath.Base(clean)
	parent := filepath.Dir(clean)
	scope := "project"
	projectRoot := parent
	switch filepath.Base(parent) {
	case "applications":
		scope = "app"
		projectRoot = filepath.Dir(parent)
	case "core":
		scope = "core"
		projectRoot = filepath.Dir(parent)
	}

	digest := sha256.Sum256([]byte(projectRoot + "\x00" + scope + "\x00" + resourceName))
	return "redlaunch-" + scope + "-" + composeProjectSlug(resourceName) + "-" + fmt.Sprintf("%x", digest[:6])
}

// ProjectName returns the explicit Compose identity used for a managed project.
// The migration inventory command exposes this without invoking Docker.
func ProjectName(projectDir string) string {
	return composeProjectName(projectDir)
}

func composeProjectSlug(value string) string {
	var slug strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			slug.WriteRune(character)
			lastDash = false
		} else if slug.Len() > 0 && !lastDash {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 24 {
			break
		}
	}
	result := strings.Trim(slug.String(), "-")
	if result == "" {
		return "resource"
	}
	return result
}

func composeProcessEnvironment(environment []string, interpolationKeys map[string]struct{}) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, managerOnly := managerEnvironmentKeys[key]; managerOnly {
			continue
		}
		if _, hostSetting := composeHostEnvironmentKeys[key]; !hostSetting {
			if _, approvedInterpolation := interpolationKeys[key]; !approvedInterpolation {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func composeInterpolationVariables(projectDir, composeFile string) map[string]struct{} {
	contents, err := os.ReadFile(filepath.Join(projectDir, composeFile))
	if err != nil {
		return nil
	}
	variables := make(map[string]struct{})
	for index := 0; index < len(contents); index++ {
		if contents[index] != '$' || index+1 >= len(contents) {
			continue
		}
		if contents[index+1] == '$' {
			index++
			continue
		}
		start := index + 1
		if contents[start] == '{' {
			nameStart := start + 1
			closingOffset := bytes.IndexByte(contents[nameStart:], '}')
			if closingOffset < 0 {
				break
			}
			end := nameStart + closingOffset
			name := string(contents[nameStart:end])
			if separator := strings.IndexAny(name, ":?+-="); separator >= 0 {
				name = name[:separator]
			}
			if validEnvironmentVariableName(name) {
				variables[name] = struct{}{}
			}
			index = end
			continue
		}
		end := start
		for end < len(contents) && isEnvironmentVariableNameCharacter(contents[end], end == start) {
			end++
		}
		if end > start {
			name := string(contents[start:end])
			if validEnvironmentVariableName(name) {
				variables[name] = struct{}{}
			}
			index = end - 1
		}
	}
	return variables
}

func validEnvironmentVariableName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func isEnvironmentVariableNameCharacter(character byte, first bool) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || !first && character >= '0' && character <= '9'
}

func (r CommandRunner) verifyProjectOwnership(ctx context.Context, projectDir string) error {
	ctx = normalizeContext(ctx)
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	projectName := composeProjectName(projectDir)
	ps := exec.CommandContext(ctx, binary, "ps", "-a", "--filter", "label=com.docker.compose.project="+projectName, "--format", "{{.ID}}")
	ps.Dir = projectDir
	ps.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := runStructuredCommand(ps, "verify Compose project ownership", projectDir)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(output))
	if len(ids) > 0 {
		args := []string{"inspect", "--format", "{{json .Config.Labels}}"}
		args = append(args, ids...)
		inspect := exec.CommandContext(ctx, binary, args...)
		inspect.Dir = projectDir
		inspect.Env = composeProcessEnvironment(os.Environ(), nil)
		inspectOutput, err := runStructuredCommand(inspect, "inspect Compose project ownership", projectDir)
		if err != nil {
			return err
		}
		inspected := 0
		for _, line := range strings.Split(strings.TrimSpace(string(inspectOutput)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			inspected++
			var labels map[string]string
			if err := json.Unmarshal([]byte(line), &labels); err != nil {
				return fmt.Errorf("verify Compose project ownership: decode Docker labels: %w", err)
			}
			if labels["redlaunch.managed"] != "true" || labels["com.docker.compose.project"] != projectName {
				return fmt.Errorf("refusing to modify Compose project %q: existing container is not a Redlaunch-managed resource", projectName)
			}
		}
		if inspected != len(ids) {
			return fmt.Errorf("verify Compose project ownership: Docker returned labels for %d of %d containers", inspected, len(ids))
		}
	}

	volumeList := exec.CommandContext(ctx, binary, "volume", "ls", "--filter", "label=com.docker.compose.project="+projectName, "--format", "{{.Name}}")
	volumeList.Dir = projectDir
	volumeList.Env = composeProcessEnvironment(os.Environ(), nil)
	volumeOutput, err := runStructuredCommand(volumeList, "verify Compose volume ownership", projectDir)
	if err != nil {
		return err
	}
	volumes := strings.Fields(string(volumeOutput))
	if len(volumes) == 0 {
		return nil
	}

	volumeInspectArgs := []string{"volume", "inspect", "--format", "{{json .Labels}}"}
	volumeInspectArgs = append(volumeInspectArgs, volumes...)
	volumeInspect := exec.CommandContext(ctx, binary, volumeInspectArgs...)
	volumeInspect.Dir = projectDir
	volumeInspect.Env = composeProcessEnvironment(os.Environ(), nil)
	volumeInspectOutput, err := runStructuredCommand(volumeInspect, "inspect Compose volume ownership", projectDir)
	if err != nil {
		return err
	}
	inspected := 0
	for _, line := range strings.Split(strings.TrimSpace(string(volumeInspectOutput)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		inspected++
		var labels map[string]string
		if err := json.Unmarshal([]byte(line), &labels); err != nil {
			return fmt.Errorf("verify Compose volume ownership: decode Docker labels: %w", err)
		}
		if labels["com.docker.compose.project"] != projectName {
			return fmt.Errorf("refusing to modify Compose project %q: existing volume does not belong to the expected project", projectName)
		}
	}
	if inspected != len(volumes) {
		return fmt.Errorf("verify Compose volume ownership: Docker returned labels for %d of %d volumes", inspected, len(volumes))
	}
	return nil
}
