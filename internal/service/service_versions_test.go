package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type versionTestRunner struct {
	runtime       []compose.ServiceRuntime
	resolved      map[string]string
	resolveErr    error
	runningDigest string
	digestErr     error
	pullErr       error
	upErr         error
	pulled        []string
	started       []string
}

func (r *versionTestRunner) Up(context.Context, string) error { return nil }

func (r *versionTestRunner) UpService(_ context.Context, _ string, serviceName string) error {
	r.started = append(r.started, serviceName)
	return r.upErr
}

func (r *versionTestRunner) PullService(_ context.Context, _ string, serviceName string) error {
	r.pulled = append(r.pulled, serviceName)
	return r.pullErr
}

func (r *versionTestRunner) ResolveServiceImage(_ context.Context, _ string, serviceName string) (string, error) {
	if r.resolveErr != nil {
		return "", r.resolveErr
	}
	image, ok := r.resolved[serviceName]
	if !ok {
		return "", errors.New("service has no resolved image")
	}
	return image, nil
}

func (r *versionTestRunner) ListServices(context.Context, string) ([]compose.ServiceRuntime, error) {
	return r.runtime, nil
}

func (r *versionTestRunner) ContainerImageDigest(context.Context, string, string) (string, error) {
	if r.digestErr != nil {
		return "", r.digestErr
	}
	return r.runningDigest, nil
}

type versionRegistryStub struct {
	images      []application.RegistryImage
	listErr     error
	previous    map[string]string
	moveErr     error
	moveDigests []string
	removed     []string
	removeErr   error
}

func (s *versionRegistryStub) ListRegistryImages(context.Context) ([]application.RegistryImage, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.images, nil
}

func (s *versionRegistryStub) MoveTag(_ context.Context, repository, tag, digest string) (string, error) {
	s.moveDigests = append(s.moveDigests, repository+":"+tag+"="+digest)
	if s.moveErr != nil {
		return "", s.moveErr
	}
	return s.previous[repository+":"+tag], nil
}

func (s *versionRegistryStub) RemoveTag(_ context.Context, repository, tag string) error {
	s.removed = append(s.removed, repository+":"+tag)
	return s.removeErr
}

func versionTestService(t *testing.T, imageName string) (*Applications, *applicationRepositoryStub, *versionTestRunner, *versionRegistryStub) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{}
	runner := &versionTestRunner{
		resolved: map[string]string{"web": imageName},
		runtime:  []compose.ServiceRuntime{{ServiceName: "web", ContainerName: "redbolt-1-web"}},
	}
	applications, err := NewApplications(repository, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := applications.Create(t.Context(), "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	repository.services = []application.Service{{
		ID:            1,
		ApplicationID: created.ID,
		Name:          "web",
		Type:          application.ServiceTypeApplication,
		ImageName:     imageName,
	}}
	registry := &versionRegistryStub{previous: map[string]string{}}
	applications.SetVersionRegistryOperations(registry, registry, registry)
	return applications, repository, runner, registry
}

func TestApplicationsListServiceVersionsGroupsTagsAndFindsDeployedImage(t *testing.T) {
	digestBuild := "sha256:" + strings.Repeat("a", 64)
	digestLive := "sha256:" + strings.Repeat("b", 64)
	applications, _, runner, registry := versionTestService(t, "localhost:5000/acme-app:development")
	runner.runningDigest = digestLive
	registry.images = []application.RegistryImage{
		{Repository: "acme-app", Tag: "build-1", Digest: digestBuild},
		{Repository: "acme-app", Tag: "development", Digest: digestLive},
		{Repository: "other-app", Tag: "build-9", Digest: digestBuild},
	}

	versions, err := applications.ListServiceVersions(t.Context(), 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !versions.Eligible() || versions.Repository != "acme-app" || versions.DeploymentTag != "development" {
		t.Fatalf("ListServiceVersions() identity = %#v, want acme-app:development", versions)
	}
	if !versions.RegistryAvailable {
		t.Fatal("ListServiceVersions() RegistryAvailable = false, want true")
	}
	if versions.TaggedDigest != digestLive || versions.RunningDigest != digestLive {
		t.Fatalf("ListServiceVersions() digests = (%s, %s), want both %s", versions.TaggedDigest, versions.RunningDigest, digestLive)
	}
	if len(versions.Versions) != 2 {
		t.Fatalf("ListServiceVersions() versions = %#v, want 2 images of the repository", versions.Versions)
	}
	if versions.RestartPending() {
		t.Fatal("ListServiceVersions() RestartPending = true, want false when tag and container agree")
	}
}

func TestApplicationsListServiceVersionsFallsBackToRegisteredImage(t *testing.T) {
	applications, repository, runner, registry := versionTestService(t, "localhost:5000/acme-app:production")
	runner.resolveErr = errors.New("compose configuration unavailable")
	registry.images = []application.RegistryImage{}

	versions, err := applications.ListServiceVersions(t.Context(), 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	if versions.DeploymentTag != "production" {
		t.Fatalf("ListServiceVersions() deployment tag = %q, want the registered image tag", versions.DeploymentTag)
	}
	if !versions.RegistryAvailable {
		t.Fatal("ListServiceVersions() RegistryAvailable = false, want true")
	}
	if len(repository.services) != 1 {
		t.Fatal("ListServiceVersions() must not modify registered services")
	}
}

func TestApplicationsListServiceVersionsRejectsNonRegistryService(t *testing.T) {
	applications, _, runner, _ := versionTestService(t, "localhost:5000/acme-app:development")
	runner.resolved["web"] = "postgres:16"

	if _, err := applications.ListServiceVersions(t.Context(), 1, "web"); !errors.Is(err, application.ErrServiceVersionUnavailable) {
		t.Fatalf("ListServiceVersions() error = %v, want ErrServiceVersionUnavailable", err)
	}
}

func TestApplicationsListServiceVersionsDegradesWhenRegistryUnavailable(t *testing.T) {
	applications, _, _, registry := versionTestService(t, "localhost:5000/acme-app:development")
	registry.listErr = application.ErrRegistryUnavailable

	versions, err := applications.ListServiceVersions(t.Context(), 1, "web")
	if err != nil {
		t.Fatal(err)
	}
	if versions.RegistryAvailable {
		t.Fatal("ListServiceVersions() RegistryAvailable = true, want false when the registry cannot be listed")
	}
	if !versions.Eligible() {
		t.Fatal("ListServiceVersions() Eligible = false, want the deployment identity to survive")
	}
}

func TestApplicationsDeployServiceVersionMovesTagPullsAndRecreates(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	applications, _, runner, registry := versionTestService(t, "localhost:5000/acme-app:development")
	registry.previous["acme-app:development"] = "sha256:" + strings.Repeat("b", 64)

	var stages []string
	err := applications.DeployServiceVersion(t.Context(), 1, "web", digest, func(stage, message string) {
		stages = append(stages, stage+":"+message)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(registry.moveDigests, ",") != "acme-app:development="+digest {
		t.Fatalf("MoveTag calls = %v, want one move to %s", registry.moveDigests, digest)
	}
	if strings.Join(runner.pulled, ",") != "web" || strings.Join(runner.started, ",") != "web" {
		t.Fatalf("pull/start calls = (%v, %v), want one pull and one recreation", runner.pulled, runner.started)
	}
	if len(stages) != 3 || !strings.HasPrefix(stages[0], "tag:") || !strings.HasPrefix(stages[1], "pull:") || !strings.HasPrefix(stages[2], "deploy:") {
		t.Fatalf("progress stages = %v, want tag, pull and deploy", stages)
	}
}

func TestApplicationsDeployServiceVersionRestoresTagWhenPullFails(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	previous := "sha256:" + strings.Repeat("b", 64)
	applications, _, runner, registry := versionTestService(t, "localhost:5000/acme-app:development")
	registry.previous["acme-app:development"] = previous
	runner.pullErr = errors.New("pull failed")

	err := applications.DeployServiceVersion(t.Context(), 1, "web", digest, nil)
	if err == nil {
		t.Fatal("DeployServiceVersion() error = nil, want pull failure")
	}
	want := []string{"acme-app:development=" + digest, "acme-app:development=" + previous}
	if strings.Join(registry.moveDigests, ",") != strings.Join(want, ",") {
		t.Fatalf("MoveTag calls = %v, want the tag restored to %s", registry.moveDigests, previous)
	}
	if len(runner.started) != 0 {
		t.Fatalf("started services = %v, want no recreation after a failed pull", runner.started)
	}
}

func TestApplicationsDeployServiceVersionRemovesNewTagWhenRecreateFails(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	applications, _, runner, registry := versionTestService(t, "localhost:5000/acme-app:first")
	runner.upErr = errors.New("up failed")

	err := applications.DeployServiceVersion(t.Context(), 1, "web", digest, nil)
	if err == nil {
		t.Fatal("DeployServiceVersion() error = nil, want recreation failure")
	}
	if strings.Join(registry.removed, ",") != "acme-app:first" {
		t.Fatalf("RemoveTag calls = %v, want the new tag removed again", registry.removed)
	}
}

func TestApplicationsDeployServiceVersionRejectsInvalidDigest(t *testing.T) {
	applications, _, _, registry := versionTestService(t, "localhost:5000/acme-app:development")

	if err := applications.DeployServiceVersion(t.Context(), 1, "web", "not-a-digest", nil); !errors.Is(err, application.ErrRegistryDigestInvalid) {
		t.Fatalf("DeployServiceVersion() error = %v, want ErrRegistryDigestInvalid", err)
	}
	if len(registry.moveDigests) != 0 {
		t.Fatalf("MoveTag calls = %v, want none for an invalid digest", registry.moveDigests)
	}
}

func TestApplicationsListApplicationVersionsReportsTagAvailability(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	applications, repository, _, registry := versionTestService(t, "localhost:5000/acme-app:development")
	repository.services = append(repository.services, application.Service{
		ID: 2, ApplicationID: 1, Name: "worker", Type: application.ServiceTypeApplication,
		ImageName: "localhost:5000/acme-worker:development",
	}, application.Service{
		ID: 3, ApplicationID: 1, Name: "database", Type: application.ServiceTypePostgreSQL,
		ImageName: "postgres:16",
	})
	registry.images = []application.RegistryImage{
		{Repository: "acme-app", Tag: "build-1", Digest: digestA},
		{Repository: "acme-app", Tag: "development", Digest: digestA},
		{Repository: "acme-worker", Tag: "development", Digest: digestB},
	}

	versions, err := applications.ListApplicationVersions(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Services) != 2 {
		t.Fatalf("ListApplicationVersions() services = %#v, want web and worker only", versions.Services)
	}
	if len(versions.Tags) != 2 {
		t.Fatalf("ListApplicationVersions() tags = %#v, want build-1 and development", versions.Tags)
	}
	build := versions.Tags[0]
	if build.Name != "build-1" || strings.Join(build.Services, ",") != "web" || strings.Join(build.Missing, ",") != "worker" {
		t.Fatalf("ListApplicationVersions() build-1 = %#v, want web with worker skipped", build)
	}
	development := versions.Tags[1]
	if development.Name != "development" || strings.Join(development.Services, ",") != "web,worker" || len(development.Missing) != 0 {
		t.Fatalf("ListApplicationVersions() development = %#v, want both services", development)
	}
}

func TestApplicationsDeployApplicationVersionSkipsAndContinues(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	applications, repository, runner, registry := versionTestService(t, "localhost:5000/acme-app:development")
	repository.services = append(repository.services,
		application.Service{ID: 2, ApplicationID: 1, Name: "worker", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/acme-worker:development"},
		application.Service{ID: 3, ApplicationID: 1, Name: "api", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/acme-api:development"},
	)
	runner.runtime = append(runner.runtime,
		compose.ServiceRuntime{ServiceName: "worker", ContainerName: "redbolt-1-worker"},
		compose.ServiceRuntime{ServiceName: "api", ContainerName: "redbolt-1-api"},
	)
	registry.images = []application.RegistryImage{
		{Repository: "acme-app", Tag: "build-1", Digest: digest},
		{Repository: "acme-api", Tag: "build-1", Digest: digest},
	}
	registry.previous["acme-app:development"] = "sha256:" + strings.Repeat("b", 64)
	registry.previous["acme-api:development"] = "sha256:" + strings.Repeat("c", 64)

	var progressServices []string
	outcomes, err := applications.DeployApplicationVersion(t.Context(), 1, "build-1", func(serviceName, state, message string) {
		if state != application.VersionDeployStateActive {
			progressServices = append(progressServices, serviceName+":"+state)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("DeployApplicationVersion() outcomes = %#v, want 3 services", outcomes)
	}
	if outcomes[0].State != application.VersionDeployStateDeployed || outcomes[0].ServiceName != "web" {
		t.Fatalf("DeployApplicationVersion() web = %#v, want deployed", outcomes[0])
	}
	if outcomes[1].State != application.VersionDeployStateSkipped || outcomes[1].ServiceName != "worker" {
		t.Fatalf("DeployApplicationVersion() worker = %#v, want skipped", outcomes[1])
	}
	if outcomes[2].State != application.VersionDeployStateDeployed || outcomes[2].ServiceName != "api" {
		t.Fatalf("DeployApplicationVersion() api = %#v, want deployed after the skipped service", outcomes[2])
	}
	if strings.Join(progressServices, ",") != "web:deployed,worker:skipped,api:deployed" {
		t.Fatalf("progress outcomes = %v, want one terminal state per service", progressServices)
	}
	if strings.Join(runner.pulled, ",") != "web,api" {
		t.Fatalf("pulled services = %v, want web and api only", runner.pulled)
	}
}

func TestApplicationsDeployApplicationVersionContinuesAfterFailure(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	applications, repository, runner, registry := versionTestService(t, "localhost:5000/acme-app:development")
	repository.services = append(repository.services,
		application.Service{ID: 2, ApplicationID: 1, Name: "api", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/acme-api:development"},
	)
	runner.runtime = append(runner.runtime, compose.ServiceRuntime{ServiceName: "api", ContainerName: "redbolt-1-api"})
	registry.images = []application.RegistryImage{
		{Repository: "acme-app", Tag: "build-1", Digest: digest},
		{Repository: "acme-api", Tag: "build-1", Digest: digest},
	}
	runner.pullErr = errors.New("pull failed")

	outcomes, err := applications.DeployApplicationVersion(t.Context(), 1, "build-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || outcomes[0].State != application.VersionDeployStateFailed || outcomes[1].State != application.VersionDeployStateFailed {
		t.Fatalf("DeployApplicationVersion() outcomes = %#v, want both services attempted", outcomes)
	}
	if len(runner.pulled) != 2 {
		t.Fatalf("pulled services = %v, want the second service attempted after the first failure", runner.pulled)
	}
	if !strings.Contains(outcomes[0].Detail, "restored") {
		t.Fatalf("DeployApplicationVersion() detail = %q, want the tag restoration reported", outcomes[0].Detail)
	}
}

func TestApplicationsDeployApplicationVersionRequiresTag(t *testing.T) {
	applications, _, _, _ := versionTestService(t, "localhost:5000/acme-app:development")

	if _, err := applications.DeployApplicationVersion(t.Context(), 1, "  ", nil); !errors.Is(err, application.ErrVersionTagRequired) {
		t.Fatalf("DeployApplicationVersion() error = %v, want ErrVersionTagRequired", err)
	}
	if _, err := applications.DeployApplicationVersion(t.Context(), 1, "bad tag", nil); !errors.Is(err, application.ErrRegistryRepositoryInvalid) {
		t.Fatalf("DeployApplicationVersion() error = %v, want tag validation error", err)
	}
}
