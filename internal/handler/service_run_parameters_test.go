package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func runParametersForm(web *Handler, values url.Values) *http.Request {
	values.Set("csrf_token", web.csrfToken)
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/migrate/run-with-parameters", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	return request
}

func TestRunServiceWithParametersStartsJobAndPassesValidatedValues(t *testing.T) {
	applications := apiApplications()
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"confirm_run": {"on"},
		"arg":         {"--database-name", "example", "--confirm"},
		"env_key":     {"NODE_ENV"},
		"env_value":   {"development"},
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, runParametersForm(web, values))
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303: %s", recorder.Code, recorder.Body)
	}
	jobID := serviceActionJobIDFromLocation(t, recorder.Header().Get("Location"))
	if jobID == "" {
		t.Fatal("redirect did not include a service action job")
	}
	waitForServiceAction(t, web, applications, "run", 7, "migrate")
	if got := strings.Join(applications.serviceActionParameters.Args, "\x00"); got != "--database-name\x00example\x00--confirm" {
		t.Fatalf("arguments = %q, want ordered values", got)
	}
	if applications.serviceActionParameters.Env["NODE_ENV"] != "development" {
		t.Fatalf("environment = %#v, want NODE_ENV=development", applications.serviceActionParameters.Env)
	}
}

func TestRunServiceWithParametersRequiresConfirmationAndCSRF(t *testing.T) {
	applications := apiApplications()
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"arg": {"--confirm"}}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, runParametersForm(web, values))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing confirmation status = %d, want 400", recorder.Code)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("missing confirmation triggered %q", action)
	}

	values.Set("confirm_run", "on")
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/migrate/run-with-parameters", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", recorder.Code)
	}
	if action, _, _ := applications.getServiceAction(); action != "" {
		t.Fatalf("missing CSRF triggered %q", action)
	}
}

func TestRunServiceWithParametersRejectsInvalidValues(t *testing.T) {
	for name, values := range map[string]url.Values{
		"unknown environment key": {"confirm_run": {"on"}, "env_key": {"PATH"}, "env_value": {"/tmp"}},
		"missing environment key": {"confirm_run": {"on"}, "env_value": {"value"}},
		"mismatched environment":  {"confirm_run": {"on"}, "env_key": {"NODE_ENV"}},
		"duplicate environment":   {"confirm_run": {"on"}, "env_key": {"NODE_ENV", "NODE_ENV"}, "env_value": {"a", "b"}},
		"newline argument":        {"confirm_run": {"on"}, "arg": {"before\nafter"}},
	} {
		t.Run(name, func(t *testing.T) {
			applications := apiApplications()
			web, err := New(nil, applications)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, runParametersForm(web, values))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
			}
			if action, _, _ := applications.getServiceAction(); action != "" {
				t.Fatalf("invalid values triggered %q", action)
			}
		})
	}
}

func TestServiceDetailsRendersRunParametersDialog(t *testing.T) {
	applications := apiApplications()
	applications.serviceDetails = application.ServiceDetails{
		Service: application.Service{ID: 1, ApplicationID: 7, Name: "migrate", Type: application.ServiceTypeApplication},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/applications/7/services/migrate", nil)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Run with parameters",
		"data-run-parameters-dialog",
		"data-run-parameters-open",
		"data-run-argument-rows",
		"data-run-environment-rows",
		"data-run-preview",
		"name=\"confirm_run\"",
		"Arguments and environment values may be visible in host process listings",
		"<option value=\"NODE_ENV\">NODE_ENV</option>",
		"<option value=\"TZ\">TZ</option>",
		"<option value=\"LANG\">LANG</option>",
		"<option value=\"LC_ALL\">LC_ALL</option>",
		"<option value=\"LOG_LEVEL\">LOG_LEVEL</option>",
		"/static/service-run-parameters.js",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("service details page did not render %q", expected)
		}
	}
	if strings.Contains(body, "<option value=\"PATH\">") {
		t.Fatal("environment selector exposed a non-allowlisted key")
	}
}

func TestServiceActionOutputRendersEscapedTextAndTruncation(t *testing.T) {
	applications := apiApplications()
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := web.serviceActionJobs.createUnique(7, "migrate", "run")
	if err != nil {
		t.Fatal(err)
	}
	job.setRunResult(application.OneOffRunResult{
		ExitCode:  1,
		Stdout:    "<script>alert('stdout')</script>\n",
		Stderr:    "<img src=x onerror=alert(1)>\n",
		Truncated: true,
	})
	job.fail(errors.New("run failed"))

	outputURL := "/applications/7/services/migrate/action/output?id=" + url.QueryEscape(job.id)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, outputURL, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("plain output status = %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("plain output Content-Type = %q, want text/plain", contentType)
	}
	if cache := recorder.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("plain output Cache-Control = %q, want no-store", cache)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "<script>alert('stdout')</script>") || !strings.Contains(body, "--- stderr ---") {
		t.Fatalf("plain output body = %q, want both streams", body)
	}

	dialogRecorder := httptest.NewRecorder()
	dialogURL := "/applications/7/services/migrate/action/output/dialog?id=" + url.QueryEscape(job.id)
	web.Routes().ServeHTTP(dialogRecorder, httptest.NewRequest(http.MethodGet, dialogURL, nil))
	if dialogRecorder.Code != http.StatusOK {
		t.Fatalf("dialog status = %d, want 200", dialogRecorder.Code)
	}
	dialog := dialogRecorder.Body.String()
	if strings.Contains(dialog, "<script>alert") || strings.Contains(dialog, "<img src=x") {
		t.Fatalf("dialog rendered raw command output: %s", dialog)
	}
	for _, expected := range []string{"&lt;script&gt;alert(&#39;stdout&#39;)&lt;/script&gt;", "&lt;img src=x onerror=alert(1)&gt;", "Output was truncated", "data-service-output-dialog", "data-copy-target=\"service-action-output-stdout\""} {
		if !strings.Contains(dialog, expected) {
			t.Fatalf("dialog did not render %q: %s", expected, dialog)
		}
	}

	toastRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(toastRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/migrate/action/status?id="+url.QueryEscape(job.id), nil))
	toast := toastRecorder.Body.String()
	for _, expected := range []string{"Run failed (exit 1)", "View output", "Output was truncated"} {
		if !strings.Contains(toast, expected) {
			t.Fatalf("toast did not render %q: %s", expected, toast)
		}
	}
}

func TestServiceActionOutputRequiresMatchingJobAndExpires(t *testing.T) {
	applications := apiApplications()
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := web.serviceActionJobs.createUnique(7, "migrate", "run")
	if err != nil {
		t.Fatal(err)
	}
	job.setRunResult(application.OneOffRunResult{ExitCode: 0, Stdout: "ok\n"})
	job.complete()

	for _, path := range []string{
		"/applications/8/services/migrate/action/output?id=" + url.QueryEscape(job.id),
		"/applications/7/services/db/action/output?id=" + url.QueryEscape(job.id),
		"/applications/7/services/migrate/action/output?id=missing",
	} {
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, recorder.Code)
		}
	}

	job.mu.Lock()
	job.finishedAt = job.finishedAt.Add(-2 * serviceActionJobRetention)
	job.mu.Unlock()
	web.serviceActionJobs.expire(time.Now())
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/migrate/action/output?id="+url.QueryEscape(job.id), nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expired output status = %d, want 404", recorder.Code)
	}
}
