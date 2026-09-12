package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type backupHandlerFake struct {
	details      application.BackupDetails
	updatedID    int64
	updatedName  string
	updatedInput application.BackupScheduleInput
	updateErr    error
	runID        int64
	runName      string
	runErr       error
	runStarted   chan struct{}
	runRelease   chan struct{}
	restoreID    int64
	restoreName  string
	restoreFile  string
	restoreErr   error
	deleteID     int64
	deleteName   string
	deleteFile   string
	deleteErr    error
	downloadID   int64
	downloadName string
	downloadFile string
	downloadData string
	downloadMeta application.Backup
	downloadErr  error
}

func (f *backupHandlerFake) GetBackupDetails(context.Context, int64, string) (application.BackupDetails, error) {
	return f.details, nil
}

func (f *backupHandlerFake) UpdateBackupSchedule(_ context.Context, id int64, serviceName string, input application.BackupScheduleInput) error {
	f.updatedID = id
	f.updatedName = serviceName
	f.updatedInput = input
	return f.updateErr
}

func (f *backupHandlerFake) RunBackupNow(_ context.Context, id int64, serviceName string) (application.Backup, error) {
	f.runID = id
	f.runName = serviceName
	if f.runStarted != nil {
		select {
		case f.runStarted <- struct{}{}:
		default:
		}
	}
	if f.runRelease != nil {
		<-f.runRelease
	}
	return application.Backup{}, f.runErr
}

func (f *backupHandlerFake) RestoreBackup(_ context.Context, id int64, serviceName, fileName string) error {
	f.restoreID = id
	f.restoreName = serviceName
	f.restoreFile = fileName
	return f.restoreErr
}

func (f *backupHandlerFake) DeleteBackup(_ context.Context, id int64, serviceName, fileName string) error {
	f.deleteID = id
	f.deleteName = serviceName
	f.deleteFile = fileName
	return f.deleteErr
}

func (f *backupHandlerFake) OpenBackup(_ context.Context, id int64, serviceName, fileName string) (io.ReadCloser, application.Backup, error) {
	f.downloadID = id
	f.downloadName = serviceName
	f.downloadFile = fileName
	if f.downloadErr != nil {
		return nil, application.Backup{}, f.downloadErr
	}
	return io.NopCloser(strings.NewReader(f.downloadData)), f.downloadMeta, nil
}

func TestServiceDetailsRendersDatabaseBackupControls(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{
			ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL, Status: "Up 9 minutes",
		}},
	}
	backup := &backupHandlerFake{details: application.BackupDetails{
		Schedule: application.BackupSchedule{
			ServiceID:        11,
			Enabled:          true,
			ScheduleType:     application.BackupScheduleWeekly,
			Hour:             3,
			Minute:           5,
			Weekday:          "sunday",
			RetentionDays:    14,
			BackupLocation:   "/var/backups/redlaunch/status-page/db",
			LastBackupAt:     time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC),
			LastBackupStatus: "successful",
			LastBackupSize:   184 * 1024 * 1024,
		},
		Backups: []application.Backup{{
			ServiceID: 11,
			FileName:  "backup-20260901-030000.000000000Z.sql",
			CreatedAt: time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC),
			SizeBytes: 184 * 1024 * 1024,
		}},
	}}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET service details status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"Schedule backups",
		"backup-schedule-summary",
		"Weekly",
		"Sunday",
		"03:05",
		"14</code> days",
		`role="switch"`,
		`checked`,
		`option value="hourly"`,
		`option value="daily"`,
		`option value="weekly"`,
		`name="weekday"`,
		"Backup location",
		"/var/backups/redlaunch/status-page/db",
		`data-copy-value="/var/backups/redlaunch/status-page/db"`,
		`id="backup-schedule-form"`,
		"Last backup",
		"Backup history",
		"backup-schedule-dialog",
		"Save",
		"Cancel",
		"Turn off scheduled backups",
		"Successful",
		"184 MB",
		"Run backup now",
		"backup-20260901-030000.000000000Z.sql",
		"/applications/7/services/db/backups/restore",
		"/applications/7/services/db/backups/delete",
		"data-backup-delete",
		"/applications/7/services/db/backups/download?backup_file=backup-20260901-030000.000000000Z.sql",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("backup page did not render %q: %s", expected, body)
		}
	}
}

func TestServiceDetailsDisablesManualBackupWhenDatabaseServiceIsStopped(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL, Status: "Exited (0)"}},
	}
	backup := &backupHandlerFake{details: application.BackupDetails{Schedule: application.BackupSchedule{ServiceID: 11}}}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET stopped service details status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `type="button" disabled`) || !strings.Contains(body, "Start the service before running a manual backup.") {
		t.Fatalf("stopped service did not render a disabled manual backup action: %s", body)
	}
	if !strings.Contains(body, "Run a backup to see it here.") {
		t.Fatalf("empty backup history did not render its supporting copy: %s", body)
	}
	if strings.Contains(body, "backup-schedule-summary") {
		t.Fatal("disabled backup schedule rendered schedule parameters")
	}
	if strings.Contains(body, `action="/applications/7/services/db/backups/run"`) {
		t.Fatal("stopped service rendered a manual backup form")
	}
}

func TestBackupHandlerDownloadsBackup(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	content := "-- downloaded backup\n"
	fileName := "backup-20260901-030000.000000000Z.sql"
	backup := &backupHandlerFake{
		downloadData: content,
		downloadMeta: application.Backup{FileName: fileName, SizeBytes: int64(len(content))},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/applications/7/services/db/backups/download?backup_file="+url.QueryEscape(fileName), nil)
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != content {
		t.Fatalf("download body = %q, want %q", recorder.Body.String(), content)
	}
	if recorder.Header().Get("Content-Type") != "application/sql" || recorder.Header().Get("Content-Length") != "21" {
		t.Fatalf("download headers = %#v, want SQL content type and length", recorder.Header())
	}
	if recorder.Header().Get("Content-Disposition") != `attachment; filename="`+fileName+`"` {
		t.Fatalf("download disposition = %q, want attachment filename", recorder.Header().Get("Content-Disposition"))
	}
	if backup.downloadID != 7 || backup.downloadName != "db" || backup.downloadFile != fileName {
		t.Fatalf("download request = (%d, %q, %q), want backup identity", backup.downloadID, backup.downloadName, backup.downloadFile)
	}
}

func TestBackupHandlersUpdateDisableRunAndRestoreWithCSRF(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	backup := &backupHandlerFake{}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}
	makeRequest := func(path string, form url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, request)
		return recorder
	}

	update := url.Values{
		"csrf_token":     {web.csrfToken},
		"enabled":        {"off", "on"},
		"schedule_type":  {"daily"},
		"hour":           {"03"},
		"minute":         {"05"},
		"retention_days": {"14"},
	}
	if recorder := makeRequest("/applications/7/services/db/backups/schedule", update); recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/applications/7/services/db?tab=backups" {
		t.Fatalf("schedule update response = (%d, %q), want redirect to the backups tab", recorder.Code, recorder.Header().Get("Location"))
	}
	if backup.updatedID != 7 || backup.updatedName != "db" || !backup.updatedInput.Enabled || backup.updatedInput.Hour != 3 || backup.updatedInput.Minute != 5 {
		t.Fatalf("schedule update = (%d, %q, %#v), want submitted daily schedule", backup.updatedID, backup.updatedName, backup.updatedInput)
	}

	disable := url.Values{"csrf_token": {web.csrfToken}, "enabled": {"off"}}
	if recorder := makeRequest("/applications/7/services/db/backups/schedule", disable); recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/applications/7/services/db?tab=backups" {
		t.Fatalf("schedule disable response = (%d, %q), want redirect to the backups tab", recorder.Code, recorder.Header().Get("Location"))
	}
	if backup.updatedInput.Enabled {
		t.Fatalf("schedule disable input = %#v, want disabled", backup.updatedInput)
	}

	if recorder := makeRequest("/applications/7/services/db/backups/run", url.Values{"csrf_token": {web.csrfToken}}); recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/applications/7/services/db?tab=backups" || backup.runID != 7 || backup.runName != "db" {
		t.Fatalf("run backup response = (%d, %q, %d, %q), want backups-tab redirect and service identity", recorder.Code, recorder.Header().Get("Location"), backup.runID, backup.runName)
	}
	restore := url.Values{"csrf_token": {web.csrfToken}, "backup_file": {"backup-20260901-030000.000000000Z.sql"}}
	if recorder := makeRequest("/applications/7/services/db/backups/restore", restore); recorder.Code != http.StatusSeeOther || backup.restoreFile == "" {
		t.Fatalf("restore backup response = (%d, %q), want redirect and submitted file", recorder.Code, backup.restoreFile)
	}
	delete := url.Values{"csrf_token": {web.csrfToken}, "backup_file": {"backup-20260901-030000.000000000Z.sql"}}
	if recorder := makeRequest("/applications/7/services/db/backups/delete", delete); recorder.Code != http.StatusSeeOther || backup.deleteID != 7 || backup.deleteName != "db" || backup.deleteFile == "" {
		t.Fatalf("delete backup response = (%d, %d, %q, %q), want redirect and submitted backup identity", recorder.Code, backup.deleteID, backup.deleteName, backup.deleteFile)
	}
}

func TestBackupHandlerReportsStoppedServiceForManualBackup(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{
			ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL,
		}},
	}
	backup := &backupHandlerFake{runErr: application.ErrBackupServiceNotRunning}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/backups/run", strings.NewReader("csrf_token="+url.QueryEscape(web.csrfToken)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stopped manual backup status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if !strings.Contains(recorder.Body.String(), "must be running before a manual backup") {
		t.Fatalf("stopped manual backup response = %q, want running-service message", recorder.Body.String())
	}
}

func TestBackupHandlerTracksBackupBeyondRequestLifetime(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{ID: 11, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
	}
	backup := &backupHandlerFake{
		runStarted: make(chan struct{}, 1),
		runRelease: make(chan struct{}),
	}
	web, err := New(nil, &fakeSetupManager{}, applications, backup)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/backups/run", strings.NewReader("csrf_token="+url.QueryEscape(web.csrfToken)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("long-running backup status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	select {
	case <-backup.runStarted:
	case <-time.After(time.Second):
		t.Fatal("long-running backup did not start")
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	jobID := location.Query().Get("backup_job")
	if jobID == "" {
		t.Fatalf("long-running backup Location = %q, want tracked job", location.String())
	}
	statusRecorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/backups/status?id="+url.QueryEscape(jobID), nil))
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `data-state="running"`) {
		t.Fatalf("running backup status = (%d, %s), want running progress", statusRecorder.Code, statusRecorder.Body.String())
	}

	close(backup.runRelease)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job := web.backupJobs.get(7, "db", jobID)
		if job != nil && job.snapshot().State == backupJobStateComplete {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("long-running backup did not reach completion")
}

func TestServiceDetailsDoesNotRenderBackupsForCacheServices(t *testing.T) {
	applications := &fakeApplicationService{
		applications:   []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		serviceDetails: application.ServiceDetails{Service: application.Service{ID: 11, ApplicationID: 7, Name: "redis", Type: application.ServiceTypeRedis}},
	}
	web, err := New(nil, &fakeSetupManager{}, applications, &backupHandlerFake{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/redis", nil))
	if strings.Contains(recorder.Body.String(), "Scheduled backups") {
		t.Fatal("cache service details rendered database backup controls")
	}
}

func TestBackupRestoreFailureReportsRolledBackState(t *testing.T) {
	restoreErr := errors.New("restore backup: exit status 3")
	if got := backupJobOperationUserMessage(backupJobOperationRestore, restoreErr); got != "The restore was rolled back and the database was left unchanged. Review the service state and try again." {
		t.Fatalf("restore failure message = %q", got)
	}
	if got := backupJobUserMessage(restoreErr); got == "The restore was rolled back and the database was left unchanged. Review the service state and try again." {
		t.Fatalf("generic backup failure message leaks restore wording: %q", got)
	}
	if got := backupJobOperationUserMessage(backupJobOperationRestore, application.ErrBackupFormatUnsupported); got != "The selected backup is not a plain SQL dump and cannot be restored." {
		t.Fatalf("unsupported format message = %q", got)
	}
}
