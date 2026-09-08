// Package service contains application services used by the HTTP layer.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"redlaunch/internal/compose"
)

const (
	applicationsDir = "applications"
	coreDir         = "core"
	proxyDir        = "proxy"
	registryDir     = "registry"
	setupMarker     = ".setup-complete"
	varsEnvFile     = "vars.env"
	secretsEnvFile  = "secrets.env"
	envFileMode     = 0o600

	proxyCompose = `services:
  proxy:
    image: caddy:2.11.4-alpine
    container_name: redbolt-proxy
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - caddy_data:/data
      - caddy_config:/config
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
    networks:
      - redlaunch-common
    labels:
      - "redlaunch.managed=true"

networks:
  redlaunch-common:
    name: redlaunch-common
    driver: bridge

volumes:
  caddy_data:
    name: redlaunch-caddy-data
  caddy_config:
    name: redlaunch-caddy-config
`

	registryCompose = `services:
  registry:
    image: registry:3.1.1
    container_name: redbolt-registry
    restart: unless-stopped
    env_file:
      - vars.env
      - secrets.env
    ports:
      - "127.0.0.1:5000:5000"
    volumes:
      - registry_data:/var/lib/registry
    networks:
      - redlaunch-registry
    labels:
      - "redlaunch.managed=true"

networks:
  redlaunch-registry:
    name: redlaunch-registry
    driver: bridge

volumes:
  registry_data:
    name: redlaunch-registry-data
`
)

// SetupService creates the managed directory layout and installs optional core
// services selected during first-run setup.
type SetupService struct {
	projectsRoot string
	runner       composeRunner
	mu           sync.Mutex
}

type composeRunner interface {
	Up(ctx context.Context, projectDir string) error
}

// NewSetupService constructs a setup service for the configured projects root.
func NewSetupService(projectsRoot string, runner composeRunner) (*SetupService, error) {
	if strings.TrimSpace(projectsRoot) == "" {
		return nil, errors.New("projects root must not be empty")
	}

	projectsRoot = filepath.Clean(projectsRoot)
	if projectsRoot == string(filepath.Separator) {
		return nil, errors.New("projects root must not be the filesystem root")
	}
	if runner == nil {
		runner = compose.CommandRunner{}
	}

	return &SetupService{
		projectsRoot: projectsRoot,
		runner:       runner,
	}, nil
}

// Initialize creates the top-level managed directories if they do not exist.
func (s *SetupService) Initialize() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ensureDirectory(s.projectsRoot); err != nil {
		return fmt.Errorf("ensure projects root: %w", err)
	}
	for _, name := range []string{applicationsDir, coreDir} {
		if err := ensureDirectory(filepath.Join(s.projectsRoot, name)); err != nil {
			return fmt.Errorf("ensure %s directory: %w", name, err)
		}
	}
	return nil
}

// NeedsSetup reports whether the first-run setup has not been completed.
func (s *SetupService) NeedsSetup() (bool, error) {
	info, err := os.Lstat(filepath.Join(s.projectsRoot, setupMarker))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect setup marker: %w", err)
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("setup marker is not a regular file")
	}
	return false, nil
}

// Setup writes and starts the selected core services, then marks setup as
// complete only after every requested service has started successfully.
func (s *SetupService) Setup(ctx context.Context, installProxy, installRegistry bool) error {
	return s.SetupWithProgress(ctx, installProxy, installRegistry, nil)
}

// EnsureRegistry makes the local registry available for integrations that
// need to reach it from another managed core project. It is safe to call
// after first-run setup and intentionally does not alter the setup marker.
func (s *SetupService) EnsureRegistry(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initializeLocked(); err != nil {
		return err
	}
	directory := filepath.Join(s.projectsRoot, coreDir, registryDir)
	composePath := filepath.Join(directory, "compose.yaml")
	if info, err := os.Lstat(composePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("registry Compose file is not a regular file")
		}
		return s.runner.Up(ctx, directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect registry Compose file: %w", err)
	}
	return s.installRegistry(ctx, nil)
}

// SetupWithProgress performs setup and reports each active stage before it
// begins. The callback is synchronous and may be nil.
func (s *SetupService) SetupWithProgress(ctx context.Context, installProxy, installRegistry bool, progress func(stage, message string)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	reportSetupProgress(progress, "directories", "Creating managed application and core directories")
	if err := s.initializeLocked(); err != nil {
		return err
	}

	if installProxy {
		reportSetupProgress(progress, "proxy-files", "Writing the Caddy Compose project")
		if err := s.installProxy(ctx, progress); err != nil {
			return err
		}
	}
	if installRegistry {
		reportSetupProgress(progress, "registry-files", "Writing the Docker Registry Compose project")
		if err := s.installRegistry(ctx, progress); err != nil {
			return err
		}
	}

	reportSetupProgress(progress, "finalize", "Saving the completed setup state")
	if err := writeManagedFile(filepath.Join(s.projectsRoot, setupMarker), "complete\n", 0o600); err != nil {
		return fmt.Errorf("write setup marker: %w", err)
	}
	return nil
}

func reportSetupProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func (s *SetupService) initializeLocked() error {
	if err := ensureDirectory(s.projectsRoot); err != nil {
		return fmt.Errorf("ensure projects root: %w", err)
	}
	for _, name := range []string{applicationsDir, coreDir} {
		if err := ensureDirectory(filepath.Join(s.projectsRoot, name)); err != nil {
			return fmt.Errorf("ensure %s directory: %w", name, err)
		}
	}
	return nil
}

func (s *SetupService) installProxy(ctx context.Context, progress func(stage, message string)) error {
	directory := filepath.Join(s.projectsRoot, coreDir, proxyDir)
	if err := ensureDirectory(directory); err != nil {
		return fmt.Errorf("ensure proxy directory: %w", err)
	}
	if err := writeEmptyEnvironmentFiles(directory); err != nil {
		return fmt.Errorf("write proxy environment files: %w", err)
	}
	if err := writeManagedFile(filepath.Join(directory, "compose.yaml"), proxyCompose, 0o644); err != nil {
		return fmt.Errorf("write proxy Compose file: %w", err)
	}
	if err := writeManagedFile(filepath.Join(directory, "Caddyfile"), "# Routes managed by Redlaunch.\n", 0o644); err != nil {
		return fmt.Errorf("write proxy Caddyfile: %w", err)
	}
	reportSetupProgress(progress, "proxy-start", "Starting Caddy with Docker Compose")
	if err := s.runner.Up(ctx, directory); err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	return nil
}

func (s *SetupService) installRegistry(ctx context.Context, progress func(stage, message string)) error {
	directory := filepath.Join(s.projectsRoot, coreDir, registryDir)
	if err := ensureDirectory(directory); err != nil {
		return fmt.Errorf("ensure registry directory: %w", err)
	}
	if err := writeEmptyEnvironmentFiles(directory); err != nil {
		return fmt.Errorf("write registry environment files: %w", err)
	}
	if err := writeManagedFile(filepath.Join(directory, "compose.yaml"), registryCompose, 0o644); err != nil {
		return fmt.Errorf("write registry Compose file: %w", err)
	}
	reportSetupProgress(progress, "registry-start", "Starting the Docker Registry with Docker Compose")
	if err := s.runner.Up(ctx, directory); err != nil {
		return fmt.Errorf("start registry: %w", err)
	}
	return nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed directory must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("managed path is not a directory")
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return fmt.Errorf("set directory permissions: %w", err)
	}
	return nil
}

func writeEmptyEnvironmentFiles(directory string) error {
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		if err := writeManagedFile(filepath.Join(directory, name), "", envFileMode); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func writeManagedFile(path, contents string, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".redlaunch-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

// writeManagedFileInPlace updates an existing managed file without replacing
// its inode. This is required for files mounted into a running container:
// replacing the host path would leave a file bind mount attached to the old
// inode. A missing file is created atomically because there is no existing
// mount to preserve.
func writeManagedFileInPlace(path, contents string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return writeManagedFile(path, contents, mode)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed path is not a regular file")
	}

	file, err := os.OpenFile(path, os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
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
	closed = true
	return nil
}
