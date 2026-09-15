package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestEditApplicationContainerPageRendersExistingConfig(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{
			{Name: "web", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/web:latest"},
			{Name: "db", Type: application.ServiceTypePostgreSQL},
		},
		applicationContainerConfig: application.ApplicationServiceInput{
			ServiceName:   "web",
			ImageName:     "localhost:5000/web:latest",
			Entrypoint:    "/usr/local/bin/start",
			RestartPolicy: application.ApplicationRestartPolicyAlways,
			PortMappings:  []application.ApplicationPortMapping{{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/web/edit", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET edit page status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 id="page-title">Edit application container</h1>`,
		`action="/applications/7/services/web/edit"`,
		`value="web"`,
		`readonly`,
		`value="web:latest"`,
		`/usr/local/bin/start`,
		`value="8080"`,
		`Save changes`,
		`Service names cannot be changed after creation.`,
		`will be updated and the container will be recreated`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET edit page did not render %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `name="auto_start"`) {
		t.Fatalf("GET edit page rendered the create-only startup toggle: %s", body)
	}
	if strings.Contains(body, `value="localhost:5000/web:latest"`) {
		t.Fatalf("GET edit page rendered the local registry prefix in the image field: %s", body)
	}
	if strings.Count(body, `<h1`) != 1 {
		t.Fatalf("GET edit page rendered %d headings, want 1", strings.Count(body, `<h1`))
	}
}

func TestEditApplicationContainerPageRejectsDatabaseService(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{
			{Name: "db", Type: application.ServiceTypePostgreSQL},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/edit", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET database edit status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestUpdateApplicationContainerRedirectsToServiceDetails(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{
			{Name: "web", Type: application.ServiceTypeApplication, ImageName: "localhost:5000/web:latest"},
			{Name: "db", Type: application.ServiceTypePostgreSQL},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf_token":           {web.csrfToken},
		"image_name":           {"web:v2"},
		"entrypoint":           {"/usr/local/bin/start"},
		"restart_policy":       {"always"},
		"port_host":            {"8080"},
		"port_container":       {"80"},
		"port_protocol":        {"tcp"},
		"volume_source":        {"app-data"},
		"volume_target":        {"/data"},
		"volume_options":       {"rw"},
		"depends_on_service":   {"db"},
		"depends_on_condition": {"service_healthy"},
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/web/edit", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST edit status = %d, want %d (%s)", recorder.Code, http.StatusSeeOther, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/applications/7/services/web" {
		t.Fatalf("POST edit Location = %q, want /applications/7/services/web", location)
	}
	if applications.applicationContainerUpdateName != "web" || applications.applicationContainerUpdateID != 7 {
		t.Fatalf("update target = (%d, %q), want (7, web)", applications.applicationContainerUpdateID, applications.applicationContainerUpdateName)
	}
	input := applications.applicationContainerUpdateInput
	if input.ImageName != "localhost:5000/web:v2" {
		t.Fatalf("update image = %q, want localhost:5000/web:v2", input.ImageName)
	}
	if input.RestartPolicy != "always" || input.Entrypoint != "/usr/local/bin/start" {
		t.Fatalf("update input = %#v, want restart and entrypoint", input)
	}
	if len(input.DependsOn) != 1 || input.DependsOn[0].ServiceName != "db" {
		t.Fatalf("update dependencies = %#v, want db", input.DependsOn)
	}
}

func TestUpdateApplicationContainerRequiresCSRFAndValidates(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services: []application.Service{
			{Name: "web", Type: application.ServiceTypeApplication},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{
		"image_name":     {"web:v2"},
		"restart_policy": {"always"},
	}
	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/web/edit", strings.NewReader(form.Encode()))
	missingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(missing, missingRequest)
	if missing.Code != http.StatusForbidden {
		t.Fatalf("POST edit without CSRF status = %d, want %d", missing.Code, http.StatusForbidden)
	}

	badForm := url.Values{
		"csrf_token":     {web.csrfToken},
		"image_name":     {"web:v2"},
		"restart_policy": {"sometimes"},
	}
	badRequest := httptest.NewRequest(http.MethodPost, "/applications/7/services/web/edit", strings.NewReader(badForm.Encode()))
	badRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	badRecorder := httptest.NewRecorder()
	handler.ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("POST edit invalid status = %d, want %d", badRecorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(badRecorder.Body.String(), "Choose a valid restart policy.") {
		t.Fatalf("POST edit invalid did not render validation message: %s", badRecorder.Body.String())
	}
}
