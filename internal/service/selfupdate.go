// Package service contains application services used by the HTTP layer.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	selfUpdateMaxCommandOutput = 8 * 1024
)

// SelfUpdateService pulls the latest Redlaunch source from GitHub and rebuilds
// the Redlaunch container with Docker Compose. It mirrors `make update`
// (`git pull` followed by `docker compose up -d --build`) so the Settings page
// offers the same operation without SSH access.
type SelfUpdateService struct {
	directory    string
	gitBinary    string
	dockerBinary string
}

// NewSelfUpdateService constructs a self-update service for the Redlaunch
// checkout directory. Empty binaries default to git and docker resolved through
// PATH. The directory itself is validated on every update so a misconfigured
// path fails with an operator-actionable error instead of running commands in
// an unexpected location.
func NewSelfUpdateService(directory, gitBinary, dockerBinary string) (*SelfUpdateService, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("redlaunch directory must not be empty")
	}
	cleaned := filepath.Clean(strings.TrimSpace(directory))
	if cleaned == string(filepath.Separator) {
		return nil, errors.New("redlaunch directory must not be the filesystem root")
	}
	gitBinary = strings.TrimSpace(gitBinary)
	if gitBinary == "" {
		gitBinary = "git"
	}
	dockerBinary = strings.TrimSpace(dockerBinary)
	if dockerBinary == "" {
		dockerBinary = "docker"
	}
	return &SelfUpdateService{
		directory:    cleaned,
		gitBinary:    gitBinary,
		dockerBinary: dockerBinary,
	}, nil
}

// Directory returns the configured Redlaunch checkout directory.
func (s *SelfUpdateService) Directory() string {
	if s == nil {
		return ""
	}
	return s.directory
}

// Update pulls the latest version and rebuilds the Redlaunch container.
func (s *SelfUpdateService) Update(ctx context.Context) error {
	return s.UpdateWithProgress(ctx, nil)
}

// UpdateWithProgress performs the update and reports each active stage before
// it begins. The callback is synchronous and may be nil.
func (s *SelfUpdateService) UpdateWithProgress(ctx context.Context, progress func(stage, message string)) error {
	if s == nil {
		return errors.New("self-update service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	directory, err := s.validatedDirectory()
	if err != nil {
		return err
	}
	reportSelfUpdateProgress(progress, "pull", "Pulling the latest Redlaunch version from GitHub")
	if err := s.runGitPull(ctx, directory); err != nil {
		return err
	}
	reportSelfUpdateProgress(progress, "rebuild", "Rebuilding the Redlaunch container")
	if err := s.runComposeRebuild(ctx, directory); err != nil {
		return err
	}
	return nil
}

func reportSelfUpdateProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func (s *SelfUpdateService) validatedDirectory() (string, error) {
	directory := filepath.Clean(s.directory)
	if directory == string(filepath.Separator) {
		return "", errors.New("redlaunch directory must not be the filesystem root")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect Redlaunch directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Redlaunch directory must not be a symlink")
	}
	if !info.IsDir() {
		return "", errors.New("Redlaunch directory is not a directory")
	}
	for _, name := range []string{"docker-compose.yml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(directory, name)
		fileInfo, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect Compose file: %w", err)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", name)
		}
		return directory, nil
	}
	return "", errors.New("Redlaunch directory does not contain a Compose file")
}

func (s *SelfUpdateService) runGitPull(ctx context.Context, directory string) error {
	// Explicit arguments only; the directory is server-configured and
	// validated, never user-controlled. No shell is involved.
	command := exec.CommandContext(ctx, s.gitBinary, "pull", "--ff-only")
	command.Dir = directory
	// Git needs the operator's environment (HOME for SSH configuration,
	// PATH, GIT_* settings) to reach GitHub the same way `make update` does.
	command.Env = os.Environ()
	output := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("pull Redlaunch update: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
	}
	return nil
}

func (s *SelfUpdateService) runComposeRebuild(ctx context.Context, directory string) error {
	// Mirrors `make update`: rebuild images and recreate the stack detached.
	command := exec.CommandContext(ctx, s.dockerBinary, "compose", "up", "-d", "--build")
	command.Dir = directory
	command.Env = os.Environ()
	output := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("rebuild Redlaunch container: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
	}
	return nil
}

type selfUpdateTailBuffer struct {
	max      int
	contents []byte
}

func newSelfUpdateTailBuffer(max int) *selfUpdateTailBuffer {
	if max < 1 {
		max = 1
	}
	return &selfUpdateTailBuffer{max: max, contents: make([]byte, 0, max)}
}

func (b *selfUpdateTailBuffer) Write(contents []byte) (int, error) {
	if b == nil || len(contents) == 0 {
		return len(contents), nil
	}
	if len(contents) >= b.max {
		b.contents = b.contents[:b.max]
		copy(b.contents, contents[len(contents)-b.max:])
		return len(contents), nil
	}
	if overflow := len(b.contents) + len(contents) - b.max; overflow > 0 {
		copy(b.contents, b.contents[overflow:])
		b.contents = b.contents[:len(b.contents)-overflow]
	}
	b.contents = append(b.contents, contents...)
	return len(contents), nil
}

func (b *selfUpdateTailBuffer) Bytes() []byte {
	if b == nil {
		return nil
	}
	return bytes.Clone(b.contents)
}
