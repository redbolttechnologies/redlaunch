package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeGitHubActionsRepository struct {
	integrations map[int64]application.GitHubActionsIntegration
	nextID       int64
}

func (r *fakeGitHubActionsRepository) GetGitHubActions(_ context.Context, applicationID int64) (application.GitHubActionsIntegration, error) {
	item, ok := r.integrations[applicationID]
	if !ok {
		return application.GitHubActionsIntegration{}, application.ErrGitHubActionsNotConfigured
	}
	return item, nil
}

func (r *fakeGitHubActionsRepository) ListGitHubActions(context.Context) ([]application.GitHubActionsIntegration, error) {
	items := make([]application.GitHubActionsIntegration, 0, len(r.integrations))
	for _, item := range r.integrations {
		items = append(items, item)
	}
	return items, nil
}

func (r *fakeGitHubActionsRepository) SaveGitHubActions(_ context.Context, item application.GitHubActionsIntegration) (application.GitHubActionsIntegration, error) {
	if r.nextID == 0 {
		r.nextID = 1
	}
	if previous, ok := r.integrations[item.ApplicationID]; ok {
		item.ID = previous.ID
	}
	if item.ID == 0 {
		item.ID = r.nextID
		r.nextID++
	}
	r.integrations[item.ApplicationID] = item
	return item, nil
}

func (r *fakeGitHubActionsRepository) DeleteGitHubActions(_ context.Context, applicationID int64) error {
	if _, ok := r.integrations[applicationID]; !ok {
		return application.ErrGitHubActionsNotConfigured
	}
	delete(r.integrations, applicationID)
	return nil
}

type fakeGitHubActionsApplications struct {
	item     application.Application
	services []application.Service
	notFound bool
}

func (a *fakeGitHubActionsApplications) Get(context.Context, int64) (application.Application, error) {
	if a.notFound {
		return application.Application{}, application.ErrNotFound
	}
	return a.item, nil
}

func (a *fakeGitHubActionsApplications) ListServices(context.Context, int64) ([]application.Service, error) {
	return a.services, nil
}

type fakeGitHubActionsRegistry struct{ calls int }

func (r *fakeGitHubActionsRegistry) EnsureRegistry(context.Context) error {
	r.calls++
	return nil
}

type fakeGitHubActionsRunner struct {
	directories []string
	err         error
}

func (r *fakeGitHubActionsRunner) RestartProject(_ context.Context, directory string) error {
	r.directories = append(r.directories, directory)
	return r.err
}

type fakeSSHKeyGenerator struct {
	index int
}

func (g *fakeSSHKeyGenerator) Generate(context.Context, string) (string, string, error) {
	g.index++
	keyData := base64.StdEncoding.EncodeToString([]byte{byte(g.index), 1, 2, 3, 4})
	public := "ssh-ed25519 " + keyData + " generated"
	private := "private-key-" + string(rune('0'+g.index))
	return private, public, nil
}

func newTestGitHubActionsService(t *testing.T) (*GitHubActionsService, *fakeGitHubActionsRepository, *fakeGitHubActionsRunner, *fakeGitHubActionsRegistry) {
	t.Helper()
	repository := &fakeGitHubActionsRepository{integrations: make(map[int64]application.GitHubActionsIntegration)}
	applications := &fakeGitHubActionsApplications{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	runner := &fakeGitHubActionsRunner{}
	registry := &fakeGitHubActionsRegistry{}
	service, err := NewGitHubActionsService(t.TempDir(), repository, applications, registry, runner, &fakeSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, runner, registry
}

func TestGitHubActionsServiceConfigureCreatesGatewayAndTransientHandoff(t *testing.T) {
	service, repository, runner, registry := newTestGitHubActionsService(t)
	setup, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository:   "acme/status-page",
		Branch:       "master",
		Dockerfile:   "Dockerfile",
		BuildContext: ".",
		ServiceName:  "web",
		ImageName:    "status-page/web",
		ServerHost:   "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if setup.PrivateKey == "" || setup.KnownHosts == "" || setup.Workflow == "" {
		t.Fatalf("handoff has empty secret or generated content: %#v", setup)
	}
	if !strings.Contains(setup.Workflow, "localhost:5000/status-page/web:${{ github.sha }}") {
		t.Fatalf("workflow image was not rendered: %s", setup.Workflow)
	}
	if !strings.Contains(setup.Workflow, "-p \"$SSH_PORT\"") || !strings.Contains(setup.Workflow, "registry:5000") {
		t.Fatalf("workflow tunnel was not rendered: %s", setup.Workflow)
	}
	if registry.calls != 1 || len(runner.directories) != 1 {
		t.Fatalf("registry calls = %d, runner calls = %d", registry.calls, len(runner.directories))
	}
	if _, err := repository.GetGitHubActions(t.Context(), 7); err != nil {
		t.Fatal(err)
	}

	directory := runner.directories[0]
	authorized, err := os.ReadFile(filepath.Join(directory, "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(authorized), setup.PrivateKey) || !strings.Contains(string(authorized), "permitopen=\"registry:5000\"") {
		t.Fatalf("authorized_keys contains unexpected content: %s", authorized)
	}
	authorizedInfo, err := os.Stat(filepath.Join(directory, "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if got := authorizedInfo.Mode().Perm(); got != 0o644 {
		t.Fatalf("authorized_keys mode = %#o, want 0644 so the non-root SSH user can read it", got)
	}
	dockerfile, err := os.ReadFile(filepath.Join(directory, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "passwd -d redlaunch-deploy") {
		t.Fatal("gateway Dockerfile does not unlock the deploy account for public-key authentication")
	}
	secret, err := os.ReadFile(filepath.Join(directory, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secret), setup.PrivateKey) {
		t.Fatal("client private key was persisted in gateway secrets")
	}
}

func TestGitHubActionsServiceRevokeRemovesKey(t *testing.T) {
	service, repository, runner, _ := newTestGitHubActionsService(t)
	if _, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "203.0.113.10",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetGitHubActions(t.Context(), 7); !errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		t.Fatalf("integration after revoke = %v, want not configured", err)
	}
	if len(runner.directories) != 2 {
		t.Fatalf("runner calls after revoke = %d, want configure plus revoke restart", len(runner.directories))
	}
	authorized, err := os.ReadFile(filepath.Join(runner.directories[0], "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(authorized)) != "" {
		t.Fatalf("authorized_keys after revoke = %q, want empty", authorized)
	}
}

func TestGitHubActionsServiceCleanupRemovesOrphanedKeyAfterApplicationDeletion(t *testing.T) {
	service, _, runner, _ := newTestGitHubActionsService(t)
	if _, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "203.0.113.10",
	}); err != nil {
		t.Fatal(err)
	}
	applications := service.applications.(*fakeGitHubActionsApplications)
	applications.notFound = true
	if err := service.CleanupApplicationKey(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if len(runner.directories) != 2 {
		t.Fatalf("runner calls = %d, want configure plus cleanup restart", len(runner.directories))
	}
	authorized, err := os.ReadFile(filepath.Join(runner.directories[0], "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(authorized)) != "" {
		t.Fatalf("authorized_keys after cleanup = %q, want empty", authorized)
	}
}

func TestGitHubActionsServiceCleanupPreservesKeyWhenApplicationStillExists(t *testing.T) {
	service, _, runner, _ := newTestGitHubActionsService(t)
	if _, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "203.0.113.10",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.CleanupApplicationKey(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if len(runner.directories) != 1 {
		t.Fatalf("runner calls = %d, want only the configure restart", len(runner.directories))
	}
	authorized, err := os.ReadFile(filepath.Join(runner.directories[0], "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(authorized), "redlaunch-github-actions:7") {
		t.Fatalf("authorized_keys after skipped cleanup = %q, want application key preserved", authorized)
	}
}

func TestRenderGitHubActionsWorkflowQuotesPaths(t *testing.T) {
	workflow, err := renderGitHubActionsWorkflow(application.GitHubActionsIntegration{
		Branch:       "feature/release",
		Dockerfile:   "docker/Docker file",
		BuildContext: "services/api",
		ImageName:    "status-page/api",
		ServerHost:   "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"branches:\n      - \"feature/release\"",
		`--file 'docker/Docker file'`,
		`'services/api'`,
		"${{ vars.REDLAUNCH_SERVER_HOST }}",
		"${{ secrets.REDLAUNCH_DEPLOY_SSH_KEY }}",
	} {
		if !strings.Contains(workflow, expected) {
			t.Errorf("workflow does not contain %q:\n%s", expected, workflow)
		}
	}
}

func TestGitHubActionsGeneratedComposeConfig(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to validate the generated gateway Compose project")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("docker is not installed: %v", err)
	}
	service, _, runner, _ := newTestGitHubActionsService(t)
	if _, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "203.0.113.10",
	}); err != nil {
		t.Fatal(err)
	}
	if len(runner.directories) != 1 {
		t.Fatalf("runner calls = %d, want one", len(runner.directories))
	}
	command := exec.Command(docker, "compose", "-f", filepath.Join(runner.directories[0], "compose.yml"), "config", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated gateway Compose config is invalid: %v\n%s", err, output)
	}
}

func TestGitHubActionsGatewayAcceptsGeneratedKey(t *testing.T) {
	if os.Getenv("REDLAUNCH_DOCKER_CONFIG_TEST") != "1" {
		t.Skip("set REDLAUNCH_DOCKER_CONFIG_TEST=1 to build the gateway and verify SSH authentication")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("docker is not installed: %v", err)
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skipf("ssh is not installed: %v", err)
	}

	root := t.TempDir()
	repository := &fakeGitHubActionsRepository{integrations: make(map[int64]application.GitHubActionsIntegration)}
	applications := &fakeGitHubActionsApplications{
		item:     application.Application{ID: 7, Name: "Status page", FolderName: "status-page"},
		services: []application.Service{{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
	}
	runner := &fakeGitHubActionsRunner{}
	service, err := NewGitHubActionsService(root, repository, applications, &fakeGitHubActionsRegistry{}, runner, CommandSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.Configure(t.Context(), 7, application.GitHubActionsInput{
		Repository: "acme/status-page", Branch: "master", Dockerfile: "Dockerfile", BuildContext: ".", ServiceName: "web", ImageName: "status-page/web", ServerHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := runner.directories[0]
	suffix := time.Now().UnixNano()
	imageName := fmt.Sprintf("redlaunch-gateway-auth-test:%d", suffix)
	containerName := fmt.Sprintf("redlaunch-gateway-auth-test-%d", suffix)
	t.Cleanup(func() {
		_ = exec.Command(docker, "rm", "--force", containerName).Run()
		_ = exec.Command(docker, "image", "rm", "--force", imageName).Run()
	})
	if output, err := exec.Command(docker, "build", "--tag", imageName, directory).CombinedOutput(); err != nil {
		t.Fatalf("build gateway image: %v\n%s", err, output)
	}
	run := exec.Command(
		docker, "run", "--detach", "--rm", "--name", containerName,
		"--publish", "127.0.0.1::2222", "--env-file", filepath.Join(directory, "secrets.env"),
		"--volume", filepath.Join(directory, "authorized_keys")+":/etc/ssh/authorized_keys:ro", imageName,
	)
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("start gateway container: %v\n%s", err, output)
	}
	portOutput, err := exec.Command(docker, "port", containerName, "2222/tcp").Output()
	if err != nil {
		t.Fatalf("resolve gateway port: %v", err)
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(string(portOutput)))
	if err != nil {
		t.Fatalf("parse gateway port %q: %v", portOutput, err)
	}
	privateKeyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(privateKeyPath, []byte(setup.PrivateKey), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 200*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway did not start listening: %v", dialErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	sshCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(sshCtx, ssh, "-vv", "-N", "-T", "-p", port, "-i", privateKeyPath,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null", githubActionsSSHUsername+"@127.0.0.1")
	output, _ := command.CombinedOutput()
	if !strings.Contains(string(output), "Authenticated to 127.0.0.1") {
		t.Fatalf("gateway did not accept the generated public key:\n%s", output)
	}
}
