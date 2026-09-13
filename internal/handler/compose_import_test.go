package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func newComposePreviewRequest(t *testing.T, token, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", token); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("compose_file", "compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/import/preview", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func newSelectiveComposeImportRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf_token", token); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("import_selective", "1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("import_service", "web"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("import_volume", "data"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("import_network", "frontend"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("compose_file", "compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("services:\n  web:\n    image: nginx:1.27\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestComposeImportPreviewListsResources(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		preview: &application.ComposeImportPreview{
			Services: []application.ComposeImportServicePreview{
				{Name: "web", Image: "nginx:1.27", DetectedType: application.ServiceTypeApplication, Ports: []string{`"8080:80"`}},
				{Name: "db", Image: "postgres:17", DetectedType: application.ServiceTypePostgreSQL, DetectionReason: "image postgres:17 looks like PostgreSQL"},
			},
			Volumes:  []application.ComposeImportResourcePreview{{Name: "data", UsedBy: []string{"web"}}},
			Networks: []application.ComposeImportResourcePreview{{Name: "frontend", UsedBy: []string{"web"}}},
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	contents := "services:\n  web:\n    image: nginx:1.27\n  db:\n    image: postgres:17\n"
	request := newComposePreviewRequest(t, web.csrfToken, contents)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /applications/7/import/preview status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`name="import_service" value="web"`,
		`name="import_service" value="db"`,
		`name="import_volume" value="data"`,
		`name="import_network" value="frontend"`,
		`nginx:1.27`,
		`postgres:17`,
		`Database`,
		`data-compose-import-preview`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview response missing %q:\n%s", want, body)
		}
	}
}

func TestComposeImportPreviewRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, newComposePreviewRequest(t, "", "services:\n  web:\n    image: nginx:1.27\n"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /applications/7/import/preview without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if applications.previewID != 0 {
		t.Fatal("preview without CSRF reached the service")
	}
}

func TestComposeImportPreviewRendersPolicyErrorWithSummary(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		preview: &application.ComposeImportPreview{
			Services: []application.ComposeImportServicePreview{{Name: "web", Image: "nginx:1.27", DetectedType: application.ServiceTypeApplication}},
		},
		previewErr: application.ErrComposeFileInvalid,
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	request := newComposePreviewRequest(t, web.csrfToken, "services:\n  web:\n    image: nginx:1.27\n")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /applications/7/import/preview with policy error status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="web"`) {
		t.Fatalf("policy-error preview did not render summary with alert:\n%s", body)
	}
}

func TestImportDockerComposeProjectWithSelection(t *testing.T) {
	applications := &fakeApplicationService{
		applications:     []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		importedServices: []application.Service{{ID: 1, ApplicationID: 7, Name: "web"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	request := newSelectiveComposeImportRequest(t, web.csrfToken)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /applications/7/import selective status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if !applications.importSelective {
		t.Fatal("selective import did not reach the selective service method")
	}
	if len(applications.importSelection.Services) != 1 || applications.importSelection.Services[0] != "web" {
		t.Fatalf("import services = %#v, want [web]", applications.importSelection.Services)
	}
	if len(applications.importSelection.Volumes) != 1 || applications.importSelection.Volumes[0] != "data" {
		t.Fatalf("import volumes = %#v, want [data]", applications.importSelection.Volumes)
	}
	if len(applications.importSelection.Networks) != 1 || applications.importSelection.Networks[0] != "frontend" {
		t.Fatalf("import networks = %#v, want [frontend]", applications.importSelection.Networks)
	}
}
