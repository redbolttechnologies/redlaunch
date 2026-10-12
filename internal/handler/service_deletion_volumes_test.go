package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type volumeDeletionFake struct {
	*fakeApplicationService
	volumes  []application.ServiceDeletionVolume
	listErr  error
	selected chan []string
}

func (f *volumeDeletionFake) ListServiceDeletionVolumes(context.Context, int64, string) ([]application.ServiceDeletionVolume, error) {
	return f.volumes, f.listErr
}

func (f *volumeDeletionFake) DeleteServiceWithVolumes(_ context.Context, _ int64, _ string, names []string, progress func(string, string)) error {
	progress("volumes", "Deleting selected volumes")
	f.selected <- names
	return nil
}

func TestServiceDeletionVolumeChoicesAndSubmission(t *testing.T) {
	fake := &volumeDeletionFake{
		fakeApplicationService: &fakeApplicationService{},
		volumes:                []application.ServiceDeletionVolume{{Name: "data"}, {Name: `escaped<volume>`}},
		selected:               make(chan []string, 1),
	}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/delete/volumes", nil))
	body := recorder.Body.String()
	for _, want := range []string{`name="delete_volumes" value="data"`, `escaped&lt;volume&gt;`, "Unchecked volumes are retained"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, " checked") || strings.Contains(body, "<volume>") {
		t.Fatalf("unsafe/default-selected choices: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("choices can be cached")
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"db"}, "delete_volumes": {"data"}}
	request := httptest.NewRequest(http.MethodPost, "/applications/7/services/db/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder = httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case names := <-fake.selected:
		if !reflect.DeepEqual(names, []string{"data"}) {
			t.Fatalf("selected = %v", names)
		}
	case <-time.After(time.Second):
		t.Fatal("deletion did not run")
	}
}

func TestServiceDeletionVolumeChoicesFailClosed(t *testing.T) {
	fake := &volumeDeletionFake{fakeApplicationService: &fakeApplicationService{}, listErr: errors.New("sensitive internal detail")}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/delete/volumes", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "retain all volume data") || strings.Contains(body, "sensitive internal detail") || strings.Contains(body, "delete_volumes") {
		t.Fatalf("unsafe fallback: %s", body)
	}
}

func TestServiceDeletionRetryShowsLockedVolumeSelections(t *testing.T) {
	fake := &volumeDeletionFake{fakeApplicationService: &fakeApplicationService{}, volumes: []application.ServiceDeletionVolume{{Name: "data", Selected: true, Frozen: true}}}
	web, err := New(nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/applications/7/services/db/delete/volumes", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `value="data" checked disabled`) || !strings.Contains(body, "Saved choice") {
		t.Fatalf("retry choices = %s", body)
	}
}
