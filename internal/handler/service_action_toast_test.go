package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestServiceActionToastAppearsImmediatelyOnApplicationPage(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "db"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"csrf_token": {web.csrfToken}}
	post := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/start", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, post)
	if postRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303", postRecorder.Code)
	}
	jobID := serviceActionJobIDFromLocation(t, postRecorder.Header().Get("Location"))

	// The redirect target must render without blocking on the project lock:
	// even while the job is running the page shows a non-blocking toast.
	get := httptest.NewRequest(http.MethodGet, "/applications/7?service_action_job="+url.QueryEscape(jobID), nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET application page status = %d, want 200", getRecorder.Code)
	}
	body := getRecorder.Body.String()
	for _, expected := range []string{`data-toast-job`, `toasts.js`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("application page did not render toast %q: %s", expected, body)
		}
	}
	if !strings.Contains(body, "Starting service") && !strings.Contains(body, "Service start completed") {
		t.Fatalf("application page toast has unexpected state: %s", body)
	}

	waitForServiceAction(t, web, applications, "start", 7, "db")
}

func TestServiceActionConflictingOperationReturnsConflict(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := web.serviceActionJobs.createUnique(7, "db", "start"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/stop", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("conflicting POST status = %d, want 409", recorder.Code)
	}
}

func TestProxyActionToastLifecycle(t *testing.T) {
	proxy := &fakeProxyService{}
	web, err := New(nil, proxy)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Routes()

	form := url.Values{"csrf_token": {web.csrfToken}}
	post := httptest.NewRequest(http.MethodPost, "/proxy/restart", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, post)
	if postRecorder.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303", postRecorder.Code)
	}
	location := postRecorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	jobID := parsed.Query().Get("proxy_action_job")
	if jobID == "" {
		t.Fatalf("Location %q has no proxy_action_job", location)
	}

	get := httptest.NewRequest(http.MethodGet, "/proxy?proxy_action_job="+url.QueryEscape(jobID), nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET proxy status = %d, want 200", getRecorder.Code)
	}
	if body := getRecorder.Body.String(); !strings.Contains(body, `data-toast-job`) {
		t.Fatalf("proxy page did not render toast")
	}

	waitForProxyAction(t, proxy, "restart")

	status := httptest.NewRequest(http.MethodGet, "/proxy/action/status?id="+url.QueryEscape(jobID), nil)
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, status)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET proxy action status = %d, want 200", statusRecorder.Code)
	}
	if body := statusRecorder.Body.String(); !strings.Contains(body, `data-toast-job`) {
		t.Fatalf("proxy status did not render toast")
	}

	// Unknown jobs report 404 like other progress endpoints.
	missing := httptest.NewRequest(http.MethodGet, "/proxy/action/status?id=missing", nil)
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, missing)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing job status = %d, want 404", missingRecorder.Code)
	}
	_ = time.Now()
}
