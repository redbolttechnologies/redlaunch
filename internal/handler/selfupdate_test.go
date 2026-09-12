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
)

type fakeSelfUpdater struct {
	mu       sync.Mutex
	calls    int
	stages   []string
	err      error
	started  chan struct{}
	release  chan struct{}
	blocking bool
}

func (f *fakeSelfUpdater) UpdateWithProgress(ctx context.Context, progress func(stage, message string)) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.blocking && f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if progress != nil {
		progress("pull", "")
		f.mu.Lock()
		failed := f.err != nil
		f.mu.Unlock()
		if !failed {
			progress("rebuild", "")
		}
	}
	f.mu.Lock()
	if f.err == nil {
		f.stages = append(f.stages, "pull", "rebuild")
	} else {
		f.stages = append(f.stages, "pull")
	}
	err := f.err
	f.mu.Unlock()
	return err
}

func TestSettingsPageRendersUpdateSection(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h2 id="update-title">Update Redlaunch</h2>`,
		`action="/settings/update"`,
		`>Update</button>`,
		`git pull`,
		`docker compose up -d --build`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /settings did not render %q: %s", expected, body)
		}
	}
}

func TestUpdateRedlaunchRequiresCSRFAndStartsJob(t *testing.T) {
	applications := &fakeApplicationService{}
	updater := &fakeSelfUpdater{release: make(chan struct{})}
	close(updater.release)
	web, err := New(nil, applications, updater)
	if err != nil {
		t.Fatal(err)
	}

	withoutCSRF := url.Values{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(withoutCSRF.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /settings/update without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	form := url.Values{"csrf_token": {web.csrfToken}}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/update status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "/settings?update_job=") {
		t.Fatalf("POST /settings/update Location = %q, want /settings?update_job=...", location)
	}

	// The status endpoint must report the tracked job.
	jobID := strings.TrimPrefix(location, "/settings?update_job=")
	statusRecorder := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/settings/update/status?id="+url.QueryEscape(jobID), nil)
	web.Routes().ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET /settings/update/status status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	if body := statusRecorder.Body.String(); !strings.Contains(body, "Updating Redlaunch") && !strings.Contains(body, "Update complete") {
		t.Fatalf("GET /settings/update/status did not render progress: %s", body)
	}
}

func TestUpdateRedlaunchRequiresConfiguredUpdater(t *testing.T) {
	applications := &fakeApplicationService{}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("POST /settings/update without updater status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestUpdateRedlaunchReportsFailureStage(t *testing.T) {
	applications := &fakeApplicationService{}
	updater := &fakeSelfUpdater{err: errors.New("pull Redlaunch update: exit status 1: boom")}
	web, err := New(nil, applications, updater)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/update status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location := recorder.Header().Get("Location")
	jobID := strings.TrimPrefix(location, "/settings?update_job=")

	// Wait for the background job to record the failure.
	deadline := 100
	var progress *selfUpdateProgressData
	for i := 0; i < deadline; i++ {
		job := web.selfUpdateJobs.get(jobID)
		if job == nil {
			t.Fatal("self-update job not found")
		}
		snapshot := job.snapshot()
		if snapshot.State == selfUpdateStateFailed {
			progress = &snapshot
			break
		}
		if snapshot.State == selfUpdateStateComplete {
			t.Fatal("self-update job completed, want failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if progress == nil {
		t.Fatal("self-update job did not fail")
	}
	if progress.ErrorDetail == "" {
		t.Fatal("self-update failure did not record error detail")
	}
}
