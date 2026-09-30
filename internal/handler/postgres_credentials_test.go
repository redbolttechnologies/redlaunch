package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func postCredentials(t *testing.T, web *Handler, path string, form url.Values, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if csrf {
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	return recorder
}

func TestUpdatePostgresCredentialsRequiresCSRF(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":       {"missing"},
		"database_user":    {"shop"},
		"new_password":     {"secret"},
		"confirm_password": {"secret"},
	}
	recorder := postCredentials(t, web, "/applications/7/services/db/credentials", form, false)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST credentials without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("credentials action without CSRF = %q, want no action", action)
	}
}

func TestUpdatePostgresCredentialsRejectsMismatchedPasswords(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"database_user":    {"shop"},
		"new_password":     {"one"},
		"confirm_password": {"two"},
	}
	recorder := postCredentials(t, web, "/applications/7/services/db/credentials", form, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST credentials with mismatched passwords status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("credentials action with mismatched passwords = %q, want no action", action)
	}
}

func TestUpdatePostgresCredentialsRejectsInvalidUser(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"database_user":    {"has-hyphen"},
		"new_password":     {"secret"},
		"confirm_password": {"secret"},
	}
	recorder := postCredentials(t, web, "/applications/7/services/db/credentials", form, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST credentials with invalid user status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestUpdatePostgresCredentialsQueuesJobAndRedirects(t *testing.T) {
	applications := &fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}}}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token":       {web.csrfToken},
		"database_user":    {"shopowner"},
		"new_password":     {"rotated-secret"},
		"confirm_password": {"rotated-secret"},
	}
	recorder := postCredentials(t, web, "/applications/7/services/db/credentials", form, true)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST credentials status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/applications/7/services/db?service_action_job=") {
		t.Fatalf("POST credentials Location = %q, want service details with job", location)
	}
	if strings.Contains(recorder.Body.String(), "rotated-secret") {
		t.Fatal("credentials redirect leaked the new password")
	}
	waitForServiceAction(t, web, applications, "credentials", 7, "db")
}

func TestServiceDetailsOmitsCredentialsForNonPostgres(t *testing.T) {
	for _, serviceType := range []string{application.ServiceTypeRedis, application.ServiceTypeApplication} {
		applications := &fakeApplicationService{
			applications: []application.Application{{ID: 7, Name: "Shop", FolderName: "shop"}},
			services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "cache"}},
			serviceDetails: application.ServiceDetails{Service: application.Service{
				ID: 1, ApplicationID: 7, Name: "cache", Type: serviceType,
			}},
		}
		web, err := New(nil, applications)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/cache", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET cache details (%s) status = %d, want %d", serviceType, recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		if strings.Contains(body, "postgres-credentials-dialog") || strings.Contains(body, "/credentials") {
			t.Fatalf("GET cache details (%s) rendered postgres credentials UI", serviceType)
		}
	}
}
