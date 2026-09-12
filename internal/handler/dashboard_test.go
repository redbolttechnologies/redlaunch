package handler

import (
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
