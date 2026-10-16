package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeVersionService struct {
	mu sync.Mutex

	versions        application.ServiceVersions
	versionsErr     error
	listedService   string
	deployedDigest  string
	deployStageSeen []string
	deployErr       error

	appVersions      application.ApplicationVersions
	appVersionsErr   error
	bulkTag          string
	bulkProgressSeen []string
	bulkOutcomes     []application.VersionDeployOutcome
	bulkErr          error
}

func (f *fakeVersionService) ListServiceVersions(_ context.Context, _ int64, serviceName string) (application.ServiceVersions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedService = serviceName
	if f.versionsErr != nil {
		return application.ServiceVersions{}, f.versionsErr
	}
	return f.versions, nil
}

func (f *fakeVersionService) DeployServiceVersion(_ context.Context, _ int64, serviceName, digest string, progress func(string, string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if progress != nil {
		progress("pull", "Pulling the selected image")
	}
	f.deployStageSeen = append(f.deployStageSeen, serviceName)
	if f.deployErr != nil {
		return f.deployErr
	}
	f.deployedDigest = digest
	return nil
}

func (f *fakeVersionService) ListApplicationVersions(context.Context, int64) (application.ApplicationVersions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appVersionsErr != nil {
		return application.ApplicationVersions{}, f.appVersionsErr
	}
	return f.appVersions, nil
}

func (f *fakeVersionService) DeployApplicationVersion(_ context.Context, _ int64, sourceTag string, progress func(string, string, string)) ([]application.VersionDeployOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkTag = sourceTag
	if f.bulkErr != nil {
		return nil, f.bulkErr
	}
	outcomes := f.bulkOutcomes
	if outcomes == nil {
		outcomes = []application.VersionDeployOutcome{{ServiceName: "web", State: application.VersionDeployStateDeployed, Detail: "The deployment tag now points at " + sourceTag + "."}}
	}
	for _, outcome := range outcomes {
		if progress != nil {
			progress(outcome.ServiceName, outcome.State, outcome.Detail)
			f.bulkProgressSeen = append(f.bulkProgressSeen, outcome.ServiceName+":"+outcome.State)
		}
	}
	return outcomes, nil
}

func (f *fakeVersionService) deployed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployedDigest
}

func (f *fakeVersionService) bulkDeployed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bulkTag
}

func versionTestVersions() application.ServiceVersions {
	digestBuild := "sha256:" + strings.Repeat("a", 64)
	digestLive := "sha256:" + strings.Repeat("b", 64)
	return application.ServiceVersions{
		ServiceName:       "web",
		Repository:        "acme-app",
		DeploymentTag:     "development",
		TaggedDigest:      digestLive,
		RunningDigest:     digestLive,
		RegistryAvailable: true,
		Versions: []application.RegistryVersion{
			{Digest: digestLive, Tags: []string{"build-2", "development"}, PushedAt: time.Now()},
			{Digest: digestBuild, Tags: []string{"build-1"}, PushedAt: time.Now().Add(-time.Hour)},
		},
	}
}

func newVersionTestHandler(t *testing.T, versions *fakeVersionService) (*Handler, *fakeApplicationService) {
	t.Helper()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/acme-app:development"}},
		serviceDetails: application.ServiceDetails{
			Service: application.Service{ID: 1, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/acme-app:development"},
		},
	}
	web, err := New(nil, applications, versions)
	if err != nil {
		t.Fatal(err)
	}
	return web, applications
}

func postVersionForm(t *testing.T, handler http.Handler, web *Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func waitForVersionDeploy(t *testing.T, versions *fakeVersionService, want string, read func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if read() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("version deployment did not run: got %q, want %q", read(), want)
}

func TestServiceDetailsPageShowsVersionsWidgetAndDeployedFlag(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET service details status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Versions",
		"acme-app:development",
		`Switch and restart`,
		`action="/applications/7/services/web/versions/deploy"`,
		`name="digest" value="sha256:` + strings.Repeat("a", 64) + `"`,
		"Deployed",
		"Current",
		"versions.js",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("service details page is missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "restart") && strings.Contains(body, "The running container uses a different image") {
		t.Fatalf("service details page shows the restart note although tag and container agree: %s", body)
	}
}

func TestServiceDetailsPageShowsRestartPendingNote(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	versions.versions.TaggedDigest = "sha256:" + strings.Repeat("a", 64)
	versions.versions.RunningDigest = "sha256:" + strings.Repeat("b", 64)
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "The running container uses a different image") {
		t.Fatalf("service details page is missing the restart note: %s", body)
	}
}

func TestServiceDetailsPageShowsUnpushedDeploymentTagNote(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	versions.versions.TaggedDigest = ""
	versions.versions.RunningDigest = ""
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "has never been pushed") {
		t.Fatalf("service details page is missing the unpushed tag note: %s", body)
	}
}

func TestServiceDetailsPageOmitsVersionsWidgetForIneligibleService(t *testing.T) {
	versions := &fakeVersionService{versionsErr: application.ErrServiceVersionUnavailable}
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET service details status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "service-versions-widget") || strings.Contains(body, "versions/deploy") {
		t.Fatalf("service details page shows versions for an ineligible service: %s", body)
	}
}

func TestDeployServiceVersionRequiresCSRFAndValidDigest(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := newVersionTestHandler(t, versions)
	handler := web.Routes()

	form := url.Values{"csrf_token": {web.csrfToken}, "digest": {"not-a-digest"}}
	recorder := postVersionForm(t, handler, web, "/applications/7/services/web/versions/deploy", form)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid digest POST status = %d, want 400", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/web/versions/deploy", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF POST status = %d, want 403", recorder.Code)
	}
	if versions.deployed() != "" {
		t.Fatal("rejected requests must not deploy a version")
	}
}

func TestDeployServiceVersionRunsTrackedJob(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := newVersionTestHandler(t, versions)
	handler := web.Routes()

	digest := "sha256:" + strings.Repeat("b", 64)
	form := url.Values{"csrf_token": {web.csrfToken}, "digest": {digest}}
	recorder := postVersionForm(t, handler, web, "/applications/7/services/web/versions/deploy", form)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("deploy POST status = %d, want 303", recorder.Code)
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "/applications/7/services/web?service_action_job=") {
		t.Fatalf("deploy redirect = %q, want the service details page with a job", location)
	}
	waitForVersionDeploy(t, versions, digest, versions.deployed)

	jobID := serviceActionJobIDFromLocation(t, location)
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/applications/7/services/web/action/status?id="+url.QueryEscape(jobID), nil))
	if status.Code != http.StatusOK {
		t.Fatalf("job status GET status = %d, want 200", status.Code)
	}
	if body := status.Body.String(); !strings.Contains(body, "data-toast-job") {
		t.Fatalf("job status is not a toast fragment: %s", body)
	}
}

func TestApplicationDetailsPageShowsDeployVersionDialog(t *testing.T) {
	versions := &fakeVersionService{
		appVersions: application.ApplicationVersions{
			Services: []application.ServiceVersionTarget{{ServiceName: "web", Repository: "acme-app", DeploymentTag: "development"}},
			Tags: []application.ApplicationVersionTag{
				{Name: "build-1", Services: []string{"web"}},
			},
		},
	}
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET application page status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Deploy version",
		`id="application-versions-dialog"`,
		`action="/applications/7/versions/deploy"`,
		`<option value="build-1"`,
		"1 of 1 services",
		"data-versions-source",
		"versions.js",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("application page is missing %q: %s", expected, body)
		}
	}
}

func TestApplicationDetailsPageHidesDeployVersionWithoutTags(t *testing.T) {
	versions := &fakeVersionService{appVersions: application.ApplicationVersions{
		Services: []application.ServiceVersionTarget{{ServiceName: "web", Repository: "acme-app", DeploymentTag: "development"}},
	}}
	web, _ := newVersionTestHandler(t, versions)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7", nil))
	body := recorder.Body.String()
	if strings.Contains(body, "application-versions-dialog") {
		t.Fatalf("application page shows the deploy dialog without any pushed tags: %s", body)
	}
}

func TestDeployApplicationVersionValidatesTag(t *testing.T) {
	versions := &fakeVersionService{}
	web, _ := newVersionTestHandler(t, versions)
	handler := web.Routes()

	for _, tag := range []string{"", "  ", "bad tag"} {
		form := url.Values{"csrf_token": {web.csrfToken}, "tag": {tag}}
		recorder := postVersionForm(t, handler, web, "/applications/7/versions/deploy", form)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("deploy tag %q POST status = %d, want 400", tag, recorder.Code)
		}
	}
	if versions.bulkDeployed() != "" {
		t.Fatal("rejected requests must not start a deployment")
	}
}

func TestDeployApplicationVersionRunsTrackedJobWithOutcomes(t *testing.T) {
	versions := &fakeVersionService{bulkOutcomes: []application.VersionDeployOutcome{
		{ServiceName: "web", State: application.VersionDeployStateDeployed, Detail: "The deployment tag now points at build-1."},
		{ServiceName: "worker", State: application.VersionDeployStateSkipped, Detail: "The repository acme-worker has no tag build-1."},
		{ServiceName: "api", State: application.VersionDeployStateFailed, Detail: "The deployment failed and the deployment tag was restored."},
	}}
	web, _ := newVersionTestHandler(t, versions)
	handler := web.Routes()

	form := url.Values{"csrf_token": {web.csrfToken}, "tag": {"build-1"}}
	recorder := postVersionForm(t, handler, web, "/applications/7/versions/deploy", form)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("deploy POST status = %d, want 303", recorder.Code)
	}
	location := recorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	jobID := parsed.Query().Get("version_deploy_job")
	if jobID == "" {
		t.Fatalf("deploy redirect %q has no version_deploy_job", location)
	}
	waitForVersionDeploy(t, versions, "build-1", versions.bulkDeployed)

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/applications/7/versions/deploy/status?id="+url.QueryEscape(jobID), nil))
	if status.Code != http.StatusOK {
		t.Fatalf("job status GET status = %d, want 200", status.Code)
	}
	body := status.Body.String()
	for _, expected := range []string{
		"data-progress-dialog",
		"Version deployment finished",
		"1 deployed · 1 skipped · 1 failed",
		"The deployment tag now points at build-1.",
		"The repository acme-worker has no tag build-1.",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("job status is missing %q: %s", expected, body)
		}
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/applications/7?version_deploy_job="+url.QueryEscape(jobID), nil))
	if page.Code != http.StatusOK {
		t.Fatalf("GET application page status = %d, want 200", page.Code)
	}
	if !strings.Contains(page.Body.String(), "data-application-version-progress") {
		t.Fatalf("application page is missing the version progress dialog: %s", page.Body.String())
	}
}

func TestDeployApplicationVersionConflictsWithDifferentTag(t *testing.T) {
	versions := &fakeVersionService{}
	web, _ := newVersionTestHandler(t, versions)
	if _, _, err := web.applicationVersionJobs.createUnique(7, "build-1"); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {web.csrfToken}, "tag": {"build-2"}}
	recorder := postVersionForm(t, web.Routes(), web, "/applications/7/versions/deploy", form)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("conflicting deploy POST status = %d, want 409", recorder.Code)
	}
}

func TestVersionDigestShortAbbreviates(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	if got := versionDigestShort(digest); got != "sha256:"+strings.Repeat("a", 12)+"…" {
		t.Fatalf("versionDigestShort() = %q, want an abbreviated digest", got)
	}
	if got := versionDigestShort("short"); got != "short" {
		t.Fatalf("versionDigestShort(short) = %q, want the value unchanged", got)
	}
}

func TestVersionDeployUserMessageAvoidsDockerDiagnostics(t *testing.T) {
	message := versionDeployUserMessage(errors.New("docker: contains SECRET_VALUE and verbose output"))
	if strings.Contains(message, "SECRET_VALUE") || strings.Contains(message, "docker:") {
		t.Fatalf("versionDeployUserMessage() = %q, must not forward diagnostics", message)
	}
}

func TestServiceDetailsPageListsUntaggedVersionWithSwitchAction(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	versions.versions.Versions[1].Tags = nil
	web, _ := newVersionTestHandler(t, versions)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`title="` + versions.versions.Versions[1].Digest + `"`,
		`name="digest" value="` + versions.versions.Versions[1].Digest + `"`,
		`aria-controls="version-switch-dialog-1"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("untagged version is missing %q", expected)
		}
	}
}
