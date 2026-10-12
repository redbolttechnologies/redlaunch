package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"redlaunch/internal/service"
	"strings"
	"testing"

	systemmetrics "redlaunch/internal/metrics"
)

func TestDashboardMetricsScopeLabelsSource(t *testing.T) {
	if got := dashboardMetricsScope(systemmetrics.ScopeVPS); got != "VPS" {
		t.Fatalf("dashboardMetricsScope(vps) = %q, want VPS", got)
	}
	if got := dashboardMetricsScope("anything-else"); got != "manager" {
		t.Fatalf("dashboardMetricsScope(other) = %q, want manager", got)
	}
	managerDetail := dashboardMetricsScopeDetail(systemmetrics.ScopeManager)
	for _, want := range []string{"procfs", "PID", "filesystem"} {
		if !strings.Contains(strings.ToLower(managerDetail), strings.ToLower(want)) {
			t.Fatalf("manager scope detail = %q, want mention of %q", managerDetail, want)
		}
	}
	vpsDetail := dashboardMetricsScopeDetail(systemmetrics.ScopeVPS)
	if !strings.Contains(vpsDetail, "METRICS_PROC_ROOT") || !strings.Contains(vpsDetail, "METRICS_FILESYSTEM_ROOT") {
		t.Fatalf("vps scope detail = %q, want the configured host-path inputs named", vpsDetail)
	}
}

type fakeDashboardDocker struct {
	snapshot   service.DockerDashboard
	err        error
	pruneCalls int
	reclaimed  uint64
}

func (f *fakeDashboardDocker) CollectDockerDashboard(context.Context) (service.DockerDashboard, error) {
	return f.snapshot, nil
}
func (f *fakeDashboardDocker) PruneUnusedImages(context.Context) (uint64, error) {
	f.pruneCalls++
	return f.reclaimed, f.err
}

func TestDashboardPruneRequiresCSRFAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		csrf, confirm bool
		want          int
	}{
		{"missing csrf", false, true, http.StatusForbidden}, {"missing confirmation", true, false, http.StatusBadRequest}, {"confirmed", true, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docker := &fakeDashboardDocker{reclaimed: 1024 * 1024}
			web, err := New(nil, &fakeDashboardMetricsService{}, docker)
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{}
			if tc.csrf {
				form.Set("csrf_token", web.csrfToken)
			}
			if tc.confirm {
				form.Set("confirmation", "prune")
			}
			request := httptest.NewRequest(http.MethodPost, "/dashboard/images/prune", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if tc.want == http.StatusOK {
				if docker.pruneCalls != 1 || !strings.Contains(recorder.Body.String(), "Reclaimed 1.0 MB") {
					t.Fatal("prune result missing")
				}
			} else if docker.pruneCalls != 0 {
				t.Fatal("unconfirmed prune executed")
			}
		})
	}
}
func TestDashboardPruneConfirmationGETDoesNotPrune(t *testing.T) {
	docker := &fakeDashboardDocker{}
	web, err := New(nil, &fakeDashboardMetricsService{}, docker)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboard/images/prune", nil))
	if recorder.Code != http.StatusOK || docker.pruneCalls != 0 || !strings.Contains(recorder.Body.String(), "open data-prune-confirmation") {
		t.Fatal("GET must only display confirmation")
	}
}
func TestDashboardPruneErrorDoesNotExposeInfrastructureDetails(t *testing.T) {
	docker := &fakeDashboardDocker{err: errors.New("private secret diagnostic")}
	web, err := New(nil, &fakeDashboardMetricsService{}, docker)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {web.csrfToken}, "confirmation": {"prune"}}
	request := httptest.NewRequest(http.MethodPost, "/dashboard/images/prune", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if strings.Contains(recorder.Body.String(), "private secret") || !strings.Contains(recorder.Body.String(), "could not be pruned") {
		t.Fatal("unsafe prune error")
	}
}
func TestDashboardDockerMetricsSurviveHostMetricsFailure(t *testing.T) {
	web, err := New(nil, &fakeDashboardMetricsService{err: errors.New("proc unavailable")}, &fakeDashboardDocker{snapshot: service.DockerDashboard{ImageCount: 9}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboard/metrics", nil))
	if !strings.Contains(recorder.Body.String(), ">9</p>") || !strings.Contains(recorder.Body.String(), "Server metrics are temporarily unavailable") {
		t.Fatal("Docker metrics hidden on procfs failure")
	}
}

func TestDashboardDisplaysAndEscapesApplicationNames(t *testing.T) {
	docker := &fakeDashboardDocker{snapshot: service.DockerDashboard{Containers: []service.ContainerUsage{{Name: "app-web", ApplicationName: "Blog <script>alert(1)</script>", Status: "running"}}, Volumes: []service.VolumeUsage{{Name: "shared", ApplicationNames: []string{"Analytics", "Blog <script>alert(1)</script>"}}}}}
	web, err := New(nil, &fakeDashboardMetricsService{}, docker)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/dashboard/metrics"} {
		recorder := httptest.NewRecorder()
		web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || strings.Count(body, `<th scope="col">Application</th>`) != 2 || strings.Contains(body, "<script>alert(1)</script>") || !strings.Contains(body, "Blog &lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(body, "Analytics, Blog &lt;script&gt;") {
			t.Fatalf("application names unsafe or missing on %s: %s", path, body)
		}
	}
}
