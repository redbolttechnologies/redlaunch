package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"redlaunch/internal/application"
)

const (
	githubActionsGatewayDir      = "github-actions-tunnel"
	githubActionsGatewayPort     = 2222
	githubActionsSSHUsername     = "redlaunch-deploy"
	githubActionsHostKeyEnv      = "SSH_HOST_PRIVATE_KEY_B64"
	githubActionsRollbackTimeout = 30 * time.Second
)

const githubActionsGatewayCompose = `services:
  github-actions-tunnel:
    build: .
    container_name: redbolt-github-actions-tunnel
    restart: unless-stopped
    read_only: true
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - SETUID
      - SETGID
    env_file:
      - vars.env
      - secrets.env
    ports:
      - "2222:2222"
    volumes:
      - ./authorized_keys:/etc/ssh/authorized_keys:ro
    networks:
      - redlaunch-registry
    tmpfs:
      - /run/sshd
      - /run/ssh
      - /tmp
    labels:
      - "redlaunch.managed=true"
    healthcheck:
      test: ["CMD", "pidof", "sshd"]
      interval: 2s
      timeout: 2s
      retries: 15
      start_period: 1s

networks:
  redlaunch-registry:
    external: true
    name: redlaunch-registry
`

const githubActionsGatewayDockerfile = `FROM alpine:3.22

RUN apk add --no-cache openssh-server \
    && adduser -D -H -s /sbin/nologin redlaunch-deploy \
    && passwd -d redlaunch-deploy

COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /usr/local/bin/redlaunch-ssh-entrypoint
RUN chmod 755 /usr/local/bin/redlaunch-ssh-entrypoint

ENTRYPOINT ["/usr/local/bin/redlaunch-ssh-entrypoint"]
`

const githubActionsGatewaySSHConfig = `Port 2222
ListenAddress 0.0.0.0
HostKey /run/ssh/ssh_host_ed25519_key
AuthorizedKeysFile /etc/ssh/authorized_keys
AllowUsers redlaunch-deploy
AuthenticationMethods publickey
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PermitEmptyPasswords no
AllowTcpForwarding local
PermitOpen registry:5000
GatewayPorts no
X11Forwarding no
AllowAgentForwarding no
PermitTTY no
PermitUserEnvironment no
MaxSessions 0
UsePAM no
LogLevel VERBOSE
`

const githubActionsGatewayEntrypoint = `#!/bin/sh
set -eu

umask 077
mkdir -p /run/ssh /run/sshd
printf '%s' "$SSH_HOST_PRIVATE_KEY_B64" | base64 -d > /run/ssh/ssh_host_ed25519_key
chmod 600 /run/ssh/ssh_host_ed25519_key
exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
`

const githubActionsGatewayDockerignore = `vars.env
secrets.env
authorized_keys
host_key.pub
`

const githubActionsVarsEnv = `# Managed by Redlaunch. Do not edit.
SSH_USERNAME=redlaunch-deploy
SSH_GATEWAY_PORT=2222
`

const githubActionsWorkflowTemplate = `name: Build and push Redlaunch image

on:
  push:
    branches:
      - {{yamlQuote .Branch}}
  workflow_dispatch:

permissions:
  contents: read

env:
  IMAGE: localhost:5000/{{.ImageName}}:{{githubSHA}}

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    steps:
      - name: Check out repository
        uses: actions/checkout@v4

      - name: Build image
        run: docker build --file {{shellQuote .Dockerfile}} --tag "$IMAGE" {{shellQuote .BuildContext}}

      - name: Open SSH tunnel and push image
        shell: bash
        env:
          SERVER_HOST: {{githubExpression "vars.REDLAUNCH_SERVER_HOST"}}
          SERVER_USERNAME: {{githubExpression "vars.REDLAUNCH_SERVER_USERNAME"}}
          SSH_PORT: {{githubExpression "vars.REDLAUNCH_SERVER_SSH_PORT"}}
          SSH_PRIVATE_KEY: {{githubExpression "secrets.REDLAUNCH_DEPLOY_SSH_KEY"}}
          SSH_KNOWN_HOSTS: {{githubExpression "secrets.REDLAUNCH_DEPLOY_KNOWN_HOSTS"}}
        run: |
          set -euo pipefail

          test -n "$SERVER_HOST" || { echo "REDLAUNCH_SERVER_HOST is not set" >&2; exit 1; }
          test -n "$SERVER_USERNAME" || { echo "REDLAUNCH_SERVER_USERNAME is not set" >&2; exit 1; }
          test -n "$SSH_PORT" || { echo "REDLAUNCH_SERVER_SSH_PORT is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "REDLAUNCH_DEPLOY_SSH_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "REDLAUNCH_DEPLOY_KNOWN_HOSTS is not set" >&2; exit 1; }
          [[ "$SSH_PORT" =~ ^[0-9]+$ ]] && (( SSH_PORT >= 1 && SSH_PORT <= 65535 )) || { echo "REDLAUNCH_SERVER_SSH_PORT is invalid" >&2; exit 1; }
          [[ "$SERVER_HOST" =~ ^([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|[0-9A-Fa-f:]+)$ ]] || { echo "REDLAUNCH_SERVER_HOST contains invalid characters" >&2; exit 1; }
          [[ "$SERVER_USERNAME" =~ ^[A-Za-z_][A-Za-z0-9._-]*$ ]] || { echo "REDLAUNCH_SERVER_USERNAME contains invalid characters" >&2; exit 1; }

          ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
          install -d -m 700 "$ssh_dir"
          printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/id_ed25519"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
          chmod 600 "$ssh_dir/id_ed25519"
          chmod 644 "$ssh_dir/known_hosts"

          tunnel_pid=""
          cleanup() {
            if [ -n "$tunnel_pid" ]; then
              kill "$tunnel_pid" 2>/dev/null || true
              wait "$tunnel_pid" 2>/dev/null || true
            fi
          }
          trap cleanup EXIT

          ssh \
            -N -T \
            -p "$SSH_PORT" \
            -i "$ssh_dir/id_ed25519" \
            -o IdentitiesOnly=yes \
            -o BatchMode=yes \
            -o ConnectTimeout=15 \
            -o ExitOnForwardFailure=yes \
            -o StrictHostKeyChecking=yes \
            -o UserKnownHostsFile="$ssh_dir/known_hosts" \
            -o ServerAliveInterval=30 \
            -o ServerAliveCountMax=3 \
            -L "127.0.0.1:5000:registry:5000" \
            "$SERVER_USERNAME@$SERVER_HOST" &
          tunnel_pid=$!

          for attempt in $(seq 1 30); do
            if ! kill -0 "$tunnel_pid" 2>/dev/null; then
              echo "SSH tunnel exited before it was ready" >&2
              exit 1
            fi
            if curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:5000/v2/ >/dev/null; then
              break
            fi
            if [ "$attempt" -eq 30 ]; then
              echo "The registry did not respond through the SSH tunnel" >&2
              exit 1
            fi
            sleep 1
          done

          docker push "$IMAGE"
`

// GitHubActionsRepository is the persistence capability required by the
// GitHub Actions deployment use case.
type GitHubActionsRepository interface {
	GetGitHubActions(context.Context, int64) (application.GitHubActionsIntegration, error)
	ListGitHubActions(context.Context) ([]application.GitHubActionsIntegration, error)
	SaveGitHubActions(context.Context, application.GitHubActionsIntegration) (application.GitHubActionsIntegration, error)
	DeleteGitHubActions(context.Context, int64) error
}

type githubActionsApplicationRepository interface {
	Get(context.Context, int64) (application.Application, error)
	ListServices(context.Context, int64) ([]application.Service, error)
}

type githubActionsRegistryManager interface {
	EnsureRegistry(context.Context) error
}

type githubActionsComposeRunner interface {
	RestartProject(context.Context, string) error
}

// GitHubActionsProgressService is implemented by the GitHub Actions service
// to report long-running Docker provisioning stages to the web layer.
type GitHubActionsProgressService interface {
	ConfigureWithProgress(context.Context, int64, application.GitHubActionsInput, func(stage, message string)) (application.GitHubActionsSetup, error)
	RevokeWithProgress(context.Context, int64, func(stage, message string)) error
}

// SSHKeyGenerator creates an OpenSSH Ed25519 key pair. Keeping this behind an
// interface makes the service deterministic to test without persisting keys.
type SSHKeyGenerator interface {
	Generate(context.Context, string) (privateKey, publicKey string, err error)
}

// CommandSSHKeyGenerator uses the system OpenSSH key generator with explicit
// arguments. It never puts key contents on a command line.
type CommandSSHKeyGenerator struct {
	Binary string
}

func (g CommandSSHKeyGenerator) Generate(ctx context.Context, comment string) (string, string, error) {
	binary := g.Binary
	if binary == "" {
		binary = "ssh-keygen"
	}
	directory, err := os.MkdirTemp("", "redlaunch-ssh-key-")
	if err != nil {
		return "", "", fmt.Errorf("create temporary SSH key directory: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", "", fmt.Errorf("protect temporary SSH key directory: %w", err)
	}
	keyPath := filepath.Join(directory, "id_ed25519")
	command := exec.CommandContext(ctx, binary, "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", keyPath)
	if output, err := command.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("generate SSH key: %w: %s", err, strings.TrimSpace(string(output)))
	}
	privateBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("read generated SSH private key: %w", err)
	}
	publicBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return "", "", fmt.Errorf("read generated SSH public key: %w", err)
	}
	return string(privateBytes), strings.TrimSpace(string(publicBytes)), nil
}

// GitHubActionsService provisions the gateway and renders repository-specific
// workflow handoff data. Private client keys are returned only in the
// transient setup payload and are never passed to the repository or logging
// layers.
type GitHubActionsService struct {
	projectsRoot string
	repository   GitHubActionsRepository
	applications githubActionsApplicationRepository
	registry     githubActionsRegistryManager
	runner       githubActionsComposeRunner
	keyGenerator SSHKeyGenerator
	mu           sync.Mutex
}

// NewGitHubActionsService constructs the GitHub Actions deployment service.
func NewGitHubActionsService(projectsRoot string, repository GitHubActionsRepository, applications githubActionsApplicationRepository, registry githubActionsRegistryManager, runner githubActionsComposeRunner, keyGenerator SSHKeyGenerator) (*GitHubActionsService, error) {
	if strings.TrimSpace(projectsRoot) == "" {
		return nil, errors.New("projects root must not be empty")
	}
	root, err := filepath.Abs(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve projects root: %w", err)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return nil, errors.New("projects root must not be the filesystem root")
	}
	if repository == nil || applications == nil || runner == nil {
		return nil, errors.New("GitHub Actions service dependencies are incomplete")
	}
	if keyGenerator == nil {
		keyGenerator = CommandSSHKeyGenerator{}
	}
	return &GitHubActionsService{
		projectsRoot: root,
		repository:   repository,
		applications: applications,
		registry:     registry,
		runner:       runner,
		keyGenerator: keyGenerator,
	}, nil
}

// Get returns the non-secret integration for an application.
func (s *GitHubActionsService) Get(ctx context.Context, applicationID int64) (application.GitHubActionsIntegration, error) {
	return s.repository.GetGitHubActions(ctx, applicationID)
}

// Configure validates an application integration, provisions its key, and
// returns the transient GitHub handoff payload.
func (s *GitHubActionsService) Configure(ctx context.Context, applicationID int64, input application.GitHubActionsInput) (application.GitHubActionsSetup, error) {
	return s.configure(ctx, applicationID, input, nil)
}

// ConfigureWithProgress provisions an integration while reporting the active
// stage. The handoff remains transient and is returned only after the gateway
// has been rebuilt and reported healthy.
func (s *GitHubActionsService) ConfigureWithProgress(ctx context.Context, applicationID int64, input application.GitHubActionsInput, progress func(stage, message string)) (application.GitHubActionsSetup, error) {
	return s.configure(ctx, applicationID, input, progress)
}

func (s *GitHubActionsService) configure(ctx context.Context, applicationID int64, input application.GitHubActionsInput, progress func(stage, message string)) (application.GitHubActionsSetup, error) {
	reportGitHubActionsProgress(progress, "validation", "Checking the application and workflow settings")
	if applicationID < 1 {
		return application.GitHubActionsSetup{}, application.ErrNotFound
	}
	normalized, err := application.ValidateGitHubActionsInput(input)
	if err != nil {
		return application.GitHubActionsSetup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.applications.Get(ctx, applicationID); err != nil {
		return application.GitHubActionsSetup{}, err
	}
	services, err := s.applications.ListServices(ctx, applicationID)
	if err != nil {
		return application.GitHubActionsSetup{}, fmt.Errorf("list application services: %w", err)
	}
	foundService := false
	for _, service := range services {
		if service.Name == normalized.ServiceName {
			foundService = true
			break
		}
	}
	if !foundService {
		return application.GitHubActionsSetup{}, application.ErrServiceNotFound
	}

	if s.registry != nil {
		reportGitHubActionsProgress(progress, "registry", "Starting the local Docker Registry")
		if err := s.registry.EnsureRegistry(ctx); err != nil {
			return application.GitHubActionsSetup{}, fmt.Errorf("ensure local registry: %w", err)
		}
	}

	gatewayDirectory := filepath.Join(s.projectsRoot, coreDir, githubActionsGatewayDir)
	reportGitHubActionsProgress(progress, "gateway", "Preparing the restricted SSH gateway")
	if err := s.ensureGatewayFiles(ctx, gatewayDirectory); err != nil {
		return application.GitHubActionsSetup{}, err
	}
	hostPublicKey, err := readPublicKey(filepath.Join(gatewayDirectory, "host_key.pub"))
	if err != nil {
		return application.GitHubActionsSetup{}, fmt.Errorf("read gateway host key: %w", err)
	}
	hostFingerprint, err := sshKeyFingerprint(hostPublicKey)
	if err != nil {
		return application.GitHubActionsSetup{}, fmt.Errorf("fingerprint gateway host key: %w", err)
	}
	reportGitHubActionsProgress(progress, "credentials", "Generating the repository SSH credentials")
	privateKey, publicKey, err := s.keyGenerator.Generate(ctx, fmt.Sprintf("redlaunch-github-actions-%d", applicationID))
	if err != nil {
		return application.GitHubActionsSetup{}, err
	}
	fingerprint, err := sshKeyFingerprint(publicKey)
	if err != nil {
		return application.GitHubActionsSetup{}, fmt.Errorf("fingerprint generated SSH key: %w", err)
	}
	now := time.Now().UTC()
	candidate := application.GitHubActionsIntegration{
		ApplicationID:  applicationID,
		Repository:     normalized.Repository,
		Branch:         normalized.Branch,
		Dockerfile:     normalized.Dockerfile,
		BuildContext:   normalized.BuildContext,
		ServiceName:    normalized.ServiceName,
		ImageName:      normalized.ImageName,
		ServerHost:     normalized.ServerHost,
		ServerPort:     githubActionsGatewayPort,
		SSHUsername:    githubActionsSSHUsername,
		PublicKey:      publicKey,
		KeyFingerprint: fingerprint,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	workflow, err := renderGitHubActionsWorkflow(candidate)
	if err != nil {
		return application.GitHubActionsSetup{}, err
	}

	previous, previousErr := s.repository.GetGitHubActions(ctx, applicationID)
	if previousErr != nil && !errors.Is(previousErr, application.ErrGitHubActionsNotConfigured) {
		return application.GitHubActionsSetup{}, previousErr
	}
	reportGitHubActionsProgress(progress, "authorization", "Installing the repository key restrictions")
	if err := s.writeAuthorizedKeys(ctx, gatewayDirectory, candidate); err != nil {
		return application.GitHubActionsSetup{}, err
	}
	reportGitHubActionsProgress(progress, "gateway-start", "Building and starting the SSH gateway")
	if err := s.runner.RestartProject(ctx, gatewayDirectory); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), githubActionsRollbackTimeout)
		_ = s.restoreAuthorizedKeys(rollbackCtx, gatewayDirectory, previous, previousErr == nil)
		cancel()
		return application.GitHubActionsSetup{}, fmt.Errorf("start GitHub Actions tunnel: %w", err)
	}
	reportGitHubActionsProgress(progress, "metadata", "Saving the GitHub Actions integration")
	saved, err := s.repository.SaveGitHubActions(ctx, candidate)
	if err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), githubActionsRollbackTimeout)
		_ = s.restoreAuthorizedKeys(rollbackCtx, gatewayDirectory, previous, previousErr == nil)
		_ = s.runner.RestartProject(rollbackCtx, gatewayDirectory)
		cancel()
		return application.GitHubActionsSetup{}, err
	}
	knownHosts := formatKnownHosts(normalized.ServerHost, githubActionsGatewayPort, hostPublicKey)
	return application.GitHubActionsSetup{
		Integration: saved,
		PrivateKey:  privateKey,
		KnownHosts:  knownHosts,
		HostKey:     hostFingerprint,
		Workflow:    workflow,
		Instructions: []string{
			"Add the generated workflow to .github/workflows/redlaunch-push-image.yml in the selected repository.",
			"Create the displayed Actions variables and secrets in the repository settings.",
			"Run the workflow manually once, then use the resulting commit-SHA image in Redlaunch.",
		},
	}, nil
}

// RenderWorkflow renders the workflow again without exposing any private key.
func (s *GitHubActionsService) RenderWorkflow(ctx context.Context, applicationID int64) (string, error) {
	integration, err := s.repository.GetGitHubActions(ctx, applicationID)
	if err != nil {
		return "", err
	}
	return renderGitHubActionsWorkflow(integration)
}

// Revoke removes one repository key and its persisted integration.
func (s *GitHubActionsService) Revoke(ctx context.Context, applicationID int64) error {
	return s.revoke(ctx, applicationID, nil)
}

// RevokeWithProgress removes a repository key while reporting each gateway
// and persistence stage to the web layer.
func (s *GitHubActionsService) RevokeWithProgress(ctx context.Context, applicationID int64, progress func(stage, message string)) error {
	return s.revoke(ctx, applicationID, progress)
}

func (s *GitHubActionsService) revoke(ctx context.Context, applicationID int64, progress func(stage, message string)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	integration, err := s.repository.GetGitHubActions(ctx, applicationID)
	if err != nil {
		return err
	}
	gatewayDirectory := filepath.Join(s.projectsRoot, coreDir, githubActionsGatewayDir)
	reportGitHubActionsProgress(progress, "gateway", "Preparing the restricted SSH gateway")
	if err := s.ensureGatewayFiles(ctx, gatewayDirectory); err != nil {
		return err
	}
	reportGitHubActionsProgress(progress, "authorization", "Removing the repository key")
	if err := s.writeAuthorizedKeysWithout(ctx, gatewayDirectory, applicationID); err != nil {
		return err
	}
	reportGitHubActionsProgress(progress, "gateway-start", "Restarting the SSH gateway")
	if err := s.runner.RestartProject(ctx, gatewayDirectory); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), githubActionsRollbackTimeout)
		_ = s.writeAuthorizedKeys(rollbackCtx, gatewayDirectory, integration)
		cancel()
		return fmt.Errorf("restart GitHub Actions tunnel: %w", err)
	}
	reportGitHubActionsProgress(progress, "metadata", "Removing the GitHub Actions integration")
	if err := s.repository.DeleteGitHubActions(ctx, applicationID); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), githubActionsRollbackTimeout)
		_ = s.writeAuthorizedKeys(rollbackCtx, gatewayDirectory, integration)
		_ = s.runner.RestartProject(rollbackCtx, gatewayDirectory)
		cancel()
		return err
	}
	return nil
}

// CleanupApplicationKey removes a key left behind if application metadata was
// deleted concurrently with a GitHub Actions rotation. It is deliberately
// idempotent and only changes the gateway after the application no longer
// exists, so a failed application deletion keeps its active integration.
func (s *GitHubActionsService) CleanupApplicationKey(ctx context.Context, applicationID int64) error {
	if applicationID < 1 {
		return application.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.applications.Get(ctx, applicationID); err == nil {
		return nil
	} else if !errors.Is(err, application.ErrNotFound) {
		return fmt.Errorf("check application before GitHub Actions cleanup: %w", err)
	}

	gatewayDirectory := filepath.Join(s.projectsRoot, coreDir, githubActionsGatewayDir)
	authorizedPath := filepath.Join(gatewayDirectory, "authorized_keys")
	contains, err := authorizedKeyForApplication(authorizedPath, applicationID)
	if err != nil {
		return err
	}
	if !contains {
		return nil
	}
	if err := s.ensureGatewayFiles(ctx, gatewayDirectory); err != nil {
		return err
	}
	if err := s.writeAuthorizedKeysWithout(ctx, gatewayDirectory, applicationID); err != nil {
		return err
	}
	if err := s.runner.RestartProject(ctx, gatewayDirectory); err != nil {
		return fmt.Errorf("restart GitHub Actions tunnel after application cleanup: %w", err)
	}
	return nil
}

func (s *GitHubActionsService) ensureGatewayFiles(ctx context.Context, directory string) error {
	if err := ensureDirectory(filepath.Dir(directory)); err != nil {
		return fmt.Errorf("ensure core directory: %w", err)
	}
	if err := ensureDirectory(directory); err != nil {
		return fmt.Errorf("ensure GitHub Actions tunnel directory: %w", err)
	}
	files := map[string]struct {
		contents string
		mode     os.FileMode
	}{
		"compose.yml":   {githubActionsGatewayCompose, 0o644},
		"Dockerfile":    {githubActionsGatewayDockerfile, 0o644},
		"sshd_config":   {githubActionsGatewaySSHConfig, 0o644},
		"entrypoint.sh": {githubActionsGatewayEntrypoint, 0o755},
		".dockerignore": {githubActionsGatewayDockerignore, 0o644},
		"vars.env":      {githubActionsVarsEnv, envFileMode},
	}
	for name, file := range files {
		path := filepath.Join(directory, name)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("gateway file %s must not be a symlink", name)
		}
		if err := writeManagedFile(path, file.contents, file.mode); err != nil {
			return fmt.Errorf("write gateway %s: %w", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "authorized_keys")); errors.Is(err, os.ErrNotExist) {
		if err := writeManagedFile(filepath.Join(directory, "authorized_keys"), "", 0o644); err != nil {
			return fmt.Errorf("create gateway authorized keys: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect gateway authorized keys: %w", err)
	}
	secretPath := filepath.Join(directory, secretsEnvFile)
	secretSnapshot, err := snapshotManagedFile(secretPath)
	if err != nil {
		return fmt.Errorf("inspect gateway secrets: %w", err)
	}
	if !secretSnapshot.exists {
		privateKey, publicKey, err := s.keyGenerator.Generate(ctx, "redlaunch-github-actions-gateway")
		if err != nil {
			return fmt.Errorf("generate gateway host key: %w", err)
		}
		if err := writeManagedFile(filepath.Join(directory, "host_key.pub"), publicKey+"\n", 0o644); err != nil {
			return fmt.Errorf("write gateway host public key: %w", err)
		}
		contents := githubActionsHostKeyEnv + "=" + base64.StdEncoding.EncodeToString([]byte(privateKey)) + "\n"
		if err := writeManagedFile(secretPath, contents, envFileMode); err != nil {
			return fmt.Errorf("write gateway secrets: %w", err)
		}
	} else {
		encoded, err := findEnvironmentVariable(string(secretSnapshot.contents), githubActionsHostKeyEnv)
		if err != nil || encoded == "" {
			return errors.New("gateway secrets do not contain a valid host key")
		}
		if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
			return fmt.Errorf("decode gateway host key: %w", err)
		}
		if _, err := os.Stat(filepath.Join(directory, "host_key.pub")); err != nil {
			return fmt.Errorf("gateway host public key is missing: %w", err)
		}
	}
	return nil
}

func (s *GitHubActionsService) writeAuthorizedKeys(ctx context.Context, directory string, candidate application.GitHubActionsIntegration) error {
	integrations, err := s.repository.ListGitHubActions(ctx)
	if err != nil {
		return fmt.Errorf("list GitHub Actions integrations: %w", err)
	}
	byApplication := make(map[int64]application.GitHubActionsIntegration, len(integrations)+1)
	for _, integration := range integrations {
		byApplication[integration.ApplicationID] = integration
	}
	byApplication[candidate.ApplicationID] = candidate
	return writeAuthorizedKeyList(directory, byApplication)
}

func (s *GitHubActionsService) writeAuthorizedKeysWithout(ctx context.Context, directory string, applicationID int64) error {
	integrations, err := s.repository.ListGitHubActions(ctx)
	if err != nil {
		return fmt.Errorf("list GitHub Actions integrations: %w", err)
	}
	byApplication := make(map[int64]application.GitHubActionsIntegration, len(integrations))
	for _, integration := range integrations {
		if integration.ApplicationID != applicationID {
			byApplication[integration.ApplicationID] = integration
		}
	}
	return writeAuthorizedKeyList(directory, byApplication)
}

func (s *GitHubActionsService) restoreAuthorizedKeys(ctx context.Context, directory string, previous application.GitHubActionsIntegration, hadPrevious bool) error {
	if !hadPrevious {
		return s.writeAuthorizedKeysWithout(ctx, directory, previous.ApplicationID)
	}
	return s.writeAuthorizedKeys(ctx, directory, previous)
}

func writeAuthorizedKeyList(directory string, integrations map[int64]application.GitHubActionsIntegration) error {
	ids := make([]int64, 0, len(integrations))
	for id := range integrations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	var lines []string
	for _, id := range ids {
		integration := integrations[id]
		fields := strings.Fields(integration.PublicKey)
		if len(fields) < 2 || fields[0] != "ssh-ed25519" {
			return fmt.Errorf("invalid public key for application %d", id)
		}
		comment := fmt.Sprintf("redlaunch-github-actions:%d", id)
		lines = append(lines, `no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,permitopen="registry:5000" `+fields[0]+" "+fields[1]+" "+comment)
	}
	contents := ""
	if len(lines) > 0 {
		contents = strings.Join(lines, "\n") + "\n"
	}
	path := filepath.Join(directory, "authorized_keys")
	if err := writeManagedFileInPlace(path, contents, 0o644); err != nil {
		return fmt.Errorf("write gateway authorized keys: %w", err)
	}
	return nil
}

func authorizedKeyForApplication(path string, applicationID int64) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect gateway authorized keys: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("gateway authorized keys is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read gateway authorized keys: %w", err)
	}
	comment := fmt.Sprintf("redlaunch-github-actions:%d", applicationID)
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), " "+comment) {
			return true, nil
		}
	}
	return false, nil
}

func readPublicKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("public key file is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(contents))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", errors.New("gateway host key is not an Ed25519 public key")
	}
	return fields[0] + " " + fields[1], nil
}

func sshKeyFingerprint(publicKey string) (string, error) {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", errors.New("public key is not an Ed25519 key")
	}
	decoded, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", fmt.Errorf("decode public key: %w", err)
	}
	sum := sha256.Sum256(decoded)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func formatKnownHosts(host string, port int, publicKey string) string {
	hostField := host
	if port != 22 {
		hostField = fmt.Sprintf("[%s]:%d", host, port)
	}
	return hostField + " " + publicKey + "\n"
}

func renderGitHubActionsWorkflow(integration application.GitHubActionsIntegration) (string, error) {
	functions := template.FuncMap{
		"githubSHA":        func() string { return "${{ github.sha }}" },
		"githubExpression": func(value string) string { return "${{ " + value + " }}" },
		"yamlQuote":        func(value string) string { return quoteYAML(value) },
		"shellQuote":       func(value string) string { return shellQuote(value) },
	}
	tmpl, err := template.New("github-actions-workflow").Funcs(functions).Parse(githubActionsWorkflowTemplate)
	if err != nil {
		return "", fmt.Errorf("parse GitHub Actions workflow template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, integration); err != nil {
		return "", fmt.Errorf("render GitHub Actions workflow: %w", err)
	}
	return output.String(), nil
}

func reportGitHubActionsProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func quoteYAML(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
