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

	defaultSelfUpdateDockerSocket = "/var/run/docker.sock"
	defaultSelfUpdateImage        = "redlaunch:local"

	// selfUpdateHelperName is the fixed name of the detached rebuild helper.
	// A fixed name single-flights rebuilds across manager restarts (the
	// in-memory job store does not survive the restart the rebuild causes):
	// a second update refuses to start while the helper is still running
	// instead of piling up concurrent full rebuilds.
	selfUpdateHelperName = "redbolt-redlaunch-updater"
)

// SelfUpdateService pulls the latest Redlaunch source from GitHub and rebuilds
// the Redlaunch container with Docker Compose. It mirrors `make update`
// (`git pull` followed by `docker compose up -d --build`) so the Settings page
// offers the same operation without SSH access.
//
// Rebuilding the Redlaunch container from inside that same container would
// terminate the Docker CLI mid-recreate (the replacement stays `Created`
// while the old container exits). The web flow therefore only runs `git pull`
// synchronously and then hands the rebuild to a detached, auto-removed helper
// container started through the Docker socket (see QueueUpdateWithProgress).
// The helper is not part of the Compose project, so recreating the manager
// does not kill it. The synchronous Update method remains for direct host
// execution and the `selfupdate-run` operator command.
type SelfUpdateService struct {
	directory    string
	gitBinary    string
	dockerBinary string
	dockerSocket string
	updaterImage string
}

// SelfUpdateOptions configures a SelfUpdateService beyond the basic
// directory and binaries.
type SelfUpdateOptions struct {
	// Directory is the Redlaunch checkout directory. Required.
	Directory string
	// GitBinary defaults to git.
	GitBinary string
	// DockerBinary defaults to docker.
	DockerBinary string
	// DockerSocket is mounted into the detached helper so it can talk to the
	// host daemon. Defaults to /var/run/docker.sock.
	DockerSocket string
	// UpdaterImage is the image used for the detached helper. It only needs
	// the Docker CLI with the Compose plugin; the helper overrides the
	// entrypoint to run `docker compose` directly, so the image does not
	// need to contain the Redlaunch update code itself. This matters because
	// the helper boots the pre-update image by definition. Defaults to
	// redlaunch:local.
	UpdaterImage string
}

// NewSelfUpdateService constructs a self-update service for the Redlaunch
// checkout directory. Empty binaries default to git and docker resolved through
// PATH. The directory itself is validated on every update so a misconfigured
// path fails with an operator-actionable error instead of running commands in
// an unexpected location.
func NewSelfUpdateService(directory, gitBinary, dockerBinary string) (*SelfUpdateService, error) {
	return NewSelfUpdateServiceWithOptions(SelfUpdateOptions{
		Directory:    directory,
		GitBinary:    gitBinary,
		DockerBinary: dockerBinary,
	})
}

// NewSelfUpdateServiceWithOptions constructs a self-update service with
// explicit socket and helper-image settings.
func NewSelfUpdateServiceWithOptions(options SelfUpdateOptions) (*SelfUpdateService, error) {
	if strings.TrimSpace(options.Directory) == "" {
		return nil, errors.New("redlaunch directory must not be empty")
	}
	cleaned := filepath.Clean(strings.TrimSpace(options.Directory))
	if cleaned == string(filepath.Separator) {
		return nil, errors.New("redlaunch directory must not be the filesystem root")
	}
	gitBinary := strings.TrimSpace(options.GitBinary)
	if gitBinary == "" {
		gitBinary = "git"
	}
	dockerBinary := strings.TrimSpace(options.DockerBinary)
	if dockerBinary == "" {
		dockerBinary = "docker"
	}
	dockerSocket := strings.TrimSpace(options.DockerSocket)
	if dockerSocket == "" {
		dockerSocket = defaultSelfUpdateDockerSocket
	}
	if !filepath.IsAbs(dockerSocket) {
		return nil, errors.New("docker socket must be an absolute path")
	}
	updaterImage := strings.TrimSpace(options.UpdaterImage)
	if updaterImage == "" {
		updaterImage = defaultSelfUpdateImage
	}
	if !validSelfUpdateImage(updaterImage) {
		return nil, fmt.Errorf("updater image %q is invalid", updaterImage)
	}
	return &SelfUpdateService{
		directory:    cleaned,
		gitBinary:    gitBinary,
		dockerBinary: dockerBinary,
		dockerSocket: dockerSocket,
		updaterImage: updaterImage,
	}, nil
}

// validSelfUpdateImage accepts Docker image references without whitespace or
// shell metacharacters. The value is passed as a single exec argument (never
// through a shell), so this is defense in depth against a misconfigured tag
// turning into surprising Docker behavior.
func validSelfUpdateImage(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, character := range value {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if letter || digit || strings.ContainsRune("_.:/@-", character) {
			continue
		}
		return false
	}
	return true
}

// Directory returns the configured Redlaunch checkout directory.
func (s *SelfUpdateService) Directory() string {
	if s == nil {
		return ""
	}
	return s.directory
}

// Update pulls the latest version and rebuilds the Redlaunch container
// synchronously. It is for direct host execution and the `selfupdate-run`
// operator command. Never call it from the web handler: the manager would
// terminate its own rebuild mid-recreate (see QueueUpdateWithProgress).
func (s *SelfUpdateService) Update(ctx context.Context) error {
	return s.UpdateWithProgress(ctx, nil)
}

// UpdateWithProgress performs the synchronous update and reports each active
// stage before it begins. The callback is synchronous and may be nil.
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

// QueueUpdateWithProgress is the web-handler entry point. It runs `git pull`
// synchronously (safe: pulling does not touch the running container) so git
// failures surface in the settings progress dialog, then starts a detached,
// auto-removed helper container that performs the synchronous rebuild. The
// helper outlives the manager container being recreated, which is exactly
// what a synchronous `docker compose up -d --build` from inside the manager
// cannot do: the CLI would be killed mid-recreate, leaving the replacement
// `Created` and the old container `Exited`.
func (s *SelfUpdateService) QueueUpdateWithProgress(ctx context.Context, progress func(stage, message string)) error {
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
	reportSelfUpdateProgress(progress, "rebuild", "Rebuilding the Redlaunch container in the background")
	if _, err := s.startDetachedRebuild(ctx, directory); err != nil {
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
	// GIT_TERMINAL_PROMPT=0 keeps a headless update from hanging on an
	// interactive credential prompt; it fails with an error instead.
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
	// Only safe outside the manager container itself (helper or host shell).
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

// startDetachedRebuild launches an auto-removed helper container that runs
// `docker compose --project-directory <repo> up -d --build` and returns its
// container ID. All arguments are fixed or server-configured and validated;
// no shell is involved.
//
// The helper overrides the image entrypoint to run the Docker CLI directly
// instead of the Redlaunch binary. That keeps the helper working even when
// the currently deployed image predates newer Redlaunch subcommands: the
// helper boots the pre-update image by definition, so it must not depend on
// post-update code. The helper mounts the Docker socket and the checkout at
// its identical host path, so the Compose bind mounts and build context
// resolve the same way as on the host, and `-e PWD` keeps `${PWD}` Compose
// interpolation aligned with `make update`. It carries no Compose project
// labels, so recreating the manager stack never touches it.
//
// The helper has a fixed name: a rebuild already running refuses a second
// one instead of piling up concurrent full rebuilds. The in-memory job store
// cannot provide this across the manager restart the rebuild causes.
func (s *SelfUpdateService) startDetachedRebuild(ctx context.Context, directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve Redlaunch directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	running, err := s.helperRunning(ctx)
	if err != nil {
		return "", err
	}
	if running {
		return "", errors.New("a Redlaunch rebuild is already running; inspect it with `docker logs " + selfUpdateHelperName + "`")
	}
	// Best effort: drop a previous exited helper so the fixed name is free.
	// A running helper is refused above and never removed here.
	remove := exec.CommandContext(ctx, s.dockerBinary, "rm", "-f", selfUpdateHelperName)
	remove.Dir = directory
	remove.Env = os.Environ()
	removeOutput := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	remove.Stdout = removeOutput
	remove.Stderr = removeOutput
	_ = remove.Run()

	args := []string{
		"run", "--rm", "-d", "--pull", "never",
		"--name", selfUpdateHelperName,
		"--label", "redlaunch.managed=true",
		"--label", "redlaunch.updater=true",
		"-v", s.dockerSocket + ":" + s.dockerSocket,
		"-v", absolute + ":" + absolute,
		"-w", absolute,
		"-e", "PWD=" + absolute,
		"-e", "REDLAUNCH_DIR=" + absolute,
		"--entrypoint", "docker",
		s.updaterImage,
		"compose", "--project-directory", absolute, "up", "-d", "--build",
	}
	command := exec.CommandContext(ctx, s.dockerBinary, args...)
	command.Dir = directory
	command.Env = os.Environ()
	output := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("start Redlaunch rebuild helper: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
	}
	return strings.TrimSpace(string(output.Bytes())), nil
}

// helperRunning reports whether the fixed-name rebuild helper currently
// exists and is running. A missing container is not an error.
func (s *SelfUpdateService) helperRunning(ctx context.Context) (bool, error) {
	command := exec.CommandContext(ctx, s.dockerBinary, "inspect", "-f", "{{.State.Running}}", selfUpdateHelperName)
	command.Env = os.Environ()
	output := newSelfUpdateTailBuffer(selfUpdateMaxCommandOutput)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		if isDockerNoSuchContainer(output.Bytes()) {
			return false, nil
		}
		// An uninspectable daemon state fails closed: spawning another
		// rebuild next to an unknown helper risks the duplicate-container
		// pileup this check exists to prevent.
		return false, fmt.Errorf("inspect Redlaunch rebuild helper: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
	}
	return strings.EqualFold(strings.TrimSpace(string(output.Bytes())), "true"), nil
}

func isDockerNoSuchContainer(output []byte) bool {
	return strings.Contains(strings.ToLower(string(output)), "no such container")
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
