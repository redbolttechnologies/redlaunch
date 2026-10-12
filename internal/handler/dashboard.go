package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	systemmetrics "redlaunch/internal/metrics"
	"redlaunch/internal/service"
)

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}

	data := h.dashboardData(r.Context())
	data.ConfirmPrune = r.URL.Path == "/dashboard/images/prune"
	h.writeDashboardPage(w, r, http.StatusOK, data)
}

func (h *Handler) dashboardMetricsFragment(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	h.writeDashboardMetrics(w, r, http.StatusOK, h.dashboardData(r.Context()))
}

func (h *Handler) dashboardData(ctx context.Context) dashboardPageData {
	snapshot, err := h.dashboardMetrics.Collect(ctx)
	if err != nil {
		h.logger.Error("collect dashboard metrics", "error", err)
	}
	data := dashboardPageData{Metrics: snapshot}
	if err != nil {
		data.Error = "Server metrics are temporarily unavailable."
	}
	if h.dashboardDocker != nil {
		data.Docker, err = h.dashboardDocker.CollectDockerDashboard(ctx)
		if err != nil {
			data.Docker.ContainersUnavailable = true
			data.Docker.DiskUnavailable = true
			data.Docker.VolumesUnavailable = true
		}
	} else {
		data.Docker.ContainersUnavailable = true
		data.Docker.DiskUnavailable = true
		data.Docker.VolumesUnavailable = true
	}
	return data
}

func (h *Handler) dashboardPruneImages(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid pruning request.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "Invalid CSRF token.", http.StatusForbidden)
		return
	}
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil || needsSetup {
		http.Error(w, "Complete server setup before pruning images.", http.StatusServiceUnavailable)
		return
	}

	if r.FormValue("confirmation") != "prune" {
		http.Error(w, "Confirm image pruning before continuing.", http.StatusBadRequest)
		return
	}
	if h.dashboardDocker == nil {
		http.Error(w, "Docker image pruning is unavailable.", http.StatusServiceUnavailable)
		return
	}
	// Image cleanup may outlast the normal ten-second page response deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
	reclaimed, err := h.dashboardDocker.PruneUnusedImages(r.Context())
	data := h.dashboardData(r.Context())
	if err != nil {
		data.ActionError = "Unused images could not be pruned. Try again."
	} else {
		data.Notice = "Unused images pruned. Reclaimed " + dashboardSize(reclaimed) + "."
	}
	h.writeDashboardPage(w, r, http.StatusOK, data)
}

func dashboardRatio(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func (h *Handler) writeDashboardPage(w http.ResponseWriter, r *http.Request, status int, data dashboardPageData, progress ...*setupProgressData) {
	w.Header().Set("Cache-Control", "no-store")
	h.setCSRFCookie(w, r)
	page := h.shellPageData(r)
	page.ActivePage = "dashboard"
	page.DashboardPage = &data
	if len(progress) > 0 {
		page.SetupProgress = progress[0]
	}
	h.writeTemplateStatus(w, "dashboard.html", page, status)
}

func (h *Handler) writeDashboardMetrics(w http.ResponseWriter, r *http.Request, status int, data dashboardPageData) {
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "dashboard-metrics.html", pageData{
		DashboardPage: &data,
		CSRFToken:     h.csrfTokenForRequest(r),
	}, status)
}

func dashboardPercent(value float64) string {
	if value < 0 {
		value = 0
	}
	return fmt.Sprintf("%.1f%%", value)
}

func dashboardSize(bytes uint64) string {
	const unit = 1024
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value := float64(bytes)
	unitIndex := 0
	for value >= unit && unitIndex < len(units)-1 {
		value /= unit
		unitIndex++
	}
	if unitIndex == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unitIndex])
	}
	return fmt.Sprintf("%.1f %s", value, units[unitIndex])
}

func dashboardTime(value time.Time) string {
	if value.IsZero() {
		return "Unavailable"
	}
	return value.Format("2006-01-02 15:04:05 MST")
}

func dashboardTimeISO(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func dashboardMetricsScope(scope string) string {
	if scope == systemmetrics.ScopeVPS {
		return "VPS"
	}
	return "manager"
}

// dashboardMetricsScopeDetail states what the scope label actually covers so
// one label never implies a single resource scope. Aggregate CPU/memory come
// from procfs files in the configured view (host-wide inside that view),
// while the process tables only ever list the PIDs visible in this process's
// namespace and disk follows the configured filesystem root.
func dashboardMetricsScopeDetail(scope string) string {
	if scope == systemmetrics.ScopeVPS {
		return "host view from METRICS_PROC_ROOT and METRICS_FILESYSTEM_ROOT"
	}
	return "this process's view: aggregate procfs files, visible PIDs, configured filesystem"
}

func dashboardMetricAge(value time.Time) string {
	if value.IsZero() {
		return "unavailable"
	}
	age := time.Since(value)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%d min ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(age/(24*time.Hour)))
	}
}

type dashboardPageData struct {
	ConfirmPrune        bool
	Metrics             systemmetrics.Snapshot
	Docker              service.DockerDashboard
	ActionError, Notice string
	Error               string
}

// Chart helpers use a shared scale so service lines are comparable.
func dashboardTrendMax(trends []service.ServiceTrend, memory bool) float64 {
	max := 100.0
	if memory {
		max = 1
	}
	for _, trend := range trends {
		for _, sample := range trend.Samples {
			value := sample.CPU
			if memory {
				value = float64(sample.Memory)
			}
			if value > max {
				max = value
			}
		}
	}
	return max
}
func dashboardTrendPoints(samples []service.UsageSample, memory bool, max float64, trends []service.ServiceTrend) string {
	if len(samples) < 2 {
		return ""
	}
	start, end := samples[0].At, samples[len(samples)-1].At
	for _, trend := range trends {
		for _, s := range trend.Samples {
			if s.At.Before(start) {
				start = s.At
			}
			if s.At.After(end) {
				end = s.At
			}
		}
	}
	if !end.After(start) {
		return ""
	}
	var points []string
	for _, s := range samples {
		value := s.CPU
		if memory {
			value = float64(s.Memory)
		}
		x := float64(s.At.Sub(start)) / float64(end.Sub(start)) * 480
		y := 100 - value/max*100
		points = append(points, fmt.Sprintf("%.1f,%.1f", x, y))
	}
	return strings.Join(points, " ")
}
func dashboardTrendScale(max float64, memory bool) string {
	if memory {
		return dashboardSize(uint64(max))
	}
	return dashboardPercent(max)
}
