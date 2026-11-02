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

type fakeSelfUpdater struct {
	mu             sync.Mutex
	calls          int
	stages         []string
	err            error
	started        chan struct{}
	release        chan struct{}
	blocking       bool
	status         application.UpdateStatus
	state          application.UpdateState
	stateOK        bool
	reconcileState *application.UpdateState
	reconcileErr   error
}

func (f *fakeSelfUpdater) CurrentVersion() string {
	if f.status.CurrentVersion != "" {
		return f.status.CurrentVersion
	}
	return "v1.0.0"
}

func (f *fakeSelfUpdater) CheckForUpdate(context.Context) (application.UpdateStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.CurrentVersion == "" {
		f.status.CurrentVersion = "v1.0.0"
		f.status.LatestVersion = "v1.1.0"
		f.status.UpdateAvailable = true
		f.status.Compatible = true
		f.status.Release = &application.ReleaseInfo{
			Version: "v1.1.0",
			Manifest: application.ReleaseManifest{
				Version:            "v1.1.0",
				Commit:             "abc123",
				DeploymentContract: 1,
			},
		}
	}
	return f.status, nil
}

func (f *fakeSelfUpdater) QueueReleaseUpdateWithProgress(ctx context.Context, _ application.UpdateStatus, progress func(stage, message string)) error {
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
		progress("check", "")
		f.mu.Lock()
		failed := f.err != nil
		f.mu.Unlock()
		if !failed {
			progress("download", "")
			progress("verify", "")
			progress("load", "")
			progress("install", "")
		}
	}
	f.mu.Lock()
	if f.err == nil {
		f.stages = append(f.stages, "check", "download", "verify", "load", "install")
	} else {
		f.stages = append(f.stages, "check")
	}
	err := f.err
	f.mu.Unlock()
	return err
}

func (f *fakeSelfUpdater) QueueReleaseUpdateWithOperationID(ctx context.Context, _ string, status application.UpdateStatus, progress func(stage, message string)) error {
	return f.QueueReleaseUpdateWithProgress(ctx, status, progress)
}

func (f *fakeSelfUpdater) UpdateState(context.Context) (application.UpdateState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.stateOK, nil
}

func (f *fakeSelfUpdater) UpdateInProgress() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocking
}

func (f *fakeSelfUpdater) HelperRunning(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocking, nil
}

func (f *fakeSelfUpdater) ReconcileInterruptedUpdate(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reconcileState != nil {
		f.state = *f.reconcileState
	}
	return f.reconcileErr
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
		`/settings/update/check`,
		`Check for updates`,
		`without compiling on this server`,
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
	parsedLocation, err := url.Parse(location)
	if err != nil || parsedLocation.Path != "/settings" {
		t.Fatalf("POST /settings/update Location = %q, want /settings?update_job=...", location)
	}
	jobID := parsedLocation.Query().Get("update_job")
	if jobID == "" || parsedLocation.Query().Get("operation") != jobID {
		t.Fatalf("Location = %q, want bound update_job and operation", location)
	}

	// The status endpoint must report the tracked job.
	statusRecorder := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/settings/update/status?id="+url.QueryEscape(jobID)+"&operation="+url.QueryEscape(jobID), nil)
	web.Routes().ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("GET /settings/update/status status = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	if body := statusRecorder.Body.String(); !strings.Contains(body, "Updating Redlaunch") && !strings.Contains(body, "Update started") {
		t.Fatalf("GET /settings/update/status did not render progress: %s", body)
	}
}

func TestUpdateRedlaunchCheckEndpointRendersRelease(t *testing.T) {
	applications := &fakeApplicationService{}
	updater := &fakeSelfUpdater{}
	web, err := New(nil, applications, updater)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings/update/check", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings/update/check status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Current version", "Latest release", "v1.1.0", "Update to v1.1.0"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("check fragment missing %q: %s", expected, body)
		}
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
	updater := &fakeSelfUpdater{err: errors.New("release image download failed")}
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
	parsedLocation, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	jobID := parsedLocation.Query().Get("update_job")
	if jobID == "" {
		t.Fatalf("Location = %q, want update_job", location)
	}

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

func TestSelfUpdateHandoffRemainsPollable(t *testing.T) {
	store := newSelfUpdateJobStore()
	job, _, err := store.createUnique()
	if err != nil {
		t.Fatal(err)
	}
	job.completeHandoff()
	if snapshot := job.snapshot(); snapshot.State != selfUpdateStateHandoff {
		t.Fatalf("handoff state = %q, want %q", snapshot.State, selfUpdateStateHandoff)
	}
}

func TestSelfUpdateStatusUsesPersistedOperationAfterRestart(t *testing.T) {
	updater := &fakeSelfUpdater{
		stateOK: true,
		state: application.UpdateState{
			OperationID: "restarted-operation",
			Phase:       application.UpdatePhaseComplete,
			Version:     "v1.2.3",
		},
	}
	web, err := New(nil, &fakeApplicationService{}, updater)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings/update/status?id=restarted-operation&operation=restarted-operation", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("persisted status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `data-state="complete"`) {
		t.Fatalf("persisted completion not rendered: %s", body)
	}
	mismatched := httptest.NewRecorder()
	web.Routes().ServeHTTP(mismatched, httptest.NewRequest(http.MethodGet, "/settings/update/status?id=other&operation=other", nil))
	if mismatched.Code != http.StatusNotFound {
		t.Fatalf("mismatched operation status = %d, want 404", mismatched.Code)
	}
}

func TestSettingsUpdateRetryAfterHandoffUsesDurableOutcome(t *testing.T) {
	for _, outcome := range []struct {
		name         string
		phase        application.UpdateStatePhase
		acknowledged bool
		retry        bool
	}{
		{"failed", application.UpdatePhaseFailed, false, true},
		{"acknowledged-failure", application.UpdatePhaseFailed, true, true},
		{"complete", application.UpdatePhaseComplete, false, true},
		{"acknowledged-uncertain", application.UpdatePhaseUncertain, true, true},
		{"uncertain", application.UpdatePhaseUncertain, false, false},
		{"verifying", application.UpdatePhaseVerifying, false, false},
	} {
		for _, pollFirst := range []bool{false, true} {
			name := outcome.name + "/direct-post"
			if pollFirst {
				name = outcome.name + "/poll-before-post"
			}
			t.Run(name, func(t *testing.T) {
				updater := &fakeSelfUpdater{started: make(chan struct{}, 2), err: errors.New("test-only new attempt failure")}
				web, err := New(nil, &fakeApplicationService{}, updater)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := web.Shutdown(ctx); err != nil {
						t.Error(err)
					}
				})
				old, _, err := web.selfUpdateJobs.createUnique()
				if err != nil {
					t.Fatal(err)
				}
				old.completeHandoff()
				updater.stateOK = true
				updater.state = application.UpdateState{
					OperationID:  old.id,
					Revision:     4,
					Phase:        outcome.phase,
					Acknowledged: outcome.acknowledged,
					Error:        "helper outcome",
				}
				if pollFirst {
					status := httptest.NewRecorder()
					web.Routes().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/settings/update/status?id="+old.id+"&operation="+old.id, nil))
					if status.Code != http.StatusOK {
						t.Fatalf("status endpoint = %d", status.Code)
					}
					if outcome.phase == application.UpdatePhaseFailed && !strings.Contains(status.Body.String(), `data-state="failed"`) {
						t.Fatal("durable helper failure was not rendered")
					}
				}
				form := url.Values{"csrf_token": {web.csrfToken}}
				request := httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
				recorder := httptest.NewRecorder()
				web.Routes().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusSeeOther {
					t.Fatalf("update POST = %d: %s", recorder.Code, recorder.Body.String())
				}
				location, err := url.Parse(recorder.Header().Get("Location"))
				if err != nil {
					t.Fatal(err)
				}
				newID := location.Query().Get("update_job")
				if newID == "" || location.Query().Get("operation") != newID {
					t.Fatalf("operation-bound redirect missing: %s", location)
				}
				if outcome.retry {
					if newID == old.id {
						t.Fatal("retry reused a finished handoff")
					}
					select {
					case <-updater.started:
					case <-time.After(5 * time.Second):
						t.Fatal("retry did not queue a new installation")
					}
					if old.finishedAt.IsZero() {
						t.Fatal("finished handoff cannot expire")
					}
				} else {
					if newID != old.id {
						t.Fatal("active/unacknowledged uncertain operation was replaced")
					}
					updater.mu.Lock()
					calls := updater.calls
					updater.mu.Unlock()
					if calls != 0 {
						t.Fatal("protected operation queued another installation")
					}
				}
			})
		}
	}
}

func TestSettingsUpdateRecoversDeadHelperBeforeAdmission(t *testing.T) {
	for _, outcome := range []struct {
		name  string
		phase application.UpdateStatePhase
		err   error
		retry bool
	}{
		{"dead-helper-failed", application.UpdatePhaseFailed, nil, true},
		{"dead-helper-uncertain", application.UpdatePhaseUncertain, application.ErrUpdateUncertain, false},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			updater := &fakeSelfUpdater{started: make(chan struct{}, 2), err: errors.New("test-only new attempt failure")}
			web, err := New(nil, &fakeApplicationService{}, updater)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := web.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			old, _, err := web.selfUpdateJobs.createUnique()
			if err != nil {
				t.Fatal(err)
			}
			old.completeHandoff()
			reconciled := application.UpdateState{
				OperationID: old.id,
				Revision:    4,
				Phase:       outcome.phase,
				Error:       "helper disappeared",
			}
			updater.stateOK = true
			updater.state = application.UpdateState{
				OperationID: old.id,
				Revision:    3,
				Phase:       application.UpdatePhaseRestartQueued,
			}
			updater.reconcileState = &reconciled
			updater.reconcileErr = outcome.err
			form := url.Values{"csrf_token": {web.csrfToken}}
			request := httptest.NewRequest(http.MethodPost, "/settings/update", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusSeeOther {
				t.Fatalf("update POST = %d: %s", recorder.Code, recorder.Body.String())
			}
			location, err := url.Parse(recorder.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			newID := location.Query().Get("update_job")
			if outcome.retry {
				if newID == old.id {
					t.Fatal("failed dead-helper operation did not admit a retry")
				}
				select {
				case <-updater.started:
				case <-time.After(5 * time.Second):
					t.Fatal("retry did not queue a new installation")
				}
			} else {
				if newID != old.id {
					t.Fatal("uncertain dead-helper operation was replaced")
				}
				updater.mu.Lock()
				calls := updater.calls
				updater.mu.Unlock()
				if calls != 0 {
					t.Fatal("uncertain dead-helper operation queued another installation")
				}
			}
		})
	}
}

func TestHandoffReconciliationProtectsUncertaintyAndIgnoresStaleSnapshots(t *testing.T) {
	store := newSelfUpdateJobStore()
	job, _, err := store.createUnique()
	if err != nil {
		t.Fatal(err)
	}
	job.completeHandoff()
	state := application.UpdateState{OperationID: job.id, Revision: 4, Phase: application.UpdatePhaseUncertain}
	store.reconcile(state)
	store.expire(time.Now().Add(2 * selfUpdateJobRetention))
	if store.get(job.id) == nil {
		t.Fatal("unacknowledged uncertain operation expired")
	}
	if got, created, err := store.createUnique(); err != nil || created || got != job {
		t.Fatal("unacknowledged uncertain operation did not protect admission")
	}
	state.Acknowledged = true
	state.Revision++
	store.reconcile(state)
	state.Acknowledged = false
	state.Revision--
	store.reconcile(state)
	if _, created, err := store.createUnique(); err != nil || !created {
		t.Fatal("stale snapshot undid acknowledgement")
	}
}
