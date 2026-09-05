package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	systemmetrics "redlaunch/internal/metrics"
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
		return dashboardPageData{Error: "Server metrics are temporarily unavailable."}
	}
	return dashboardPageData{Metrics: snapshot}
}

func (h *Handler) writeDashboardPage(w http.ResponseWriter, r *http.Request, status int, data dashboardPageData) {
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ActivePage = "dashboard"
	page.DashboardPage = &data
	h.writeTemplateStatus(w, "dashboard.html", page, status)
}

func (h *Handler) writeDashboardMetrics(w http.ResponseWriter, _ *http.Request, status int, data dashboardPageData) {
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "dashboard-metrics.html", pageData{
		DashboardPage: &data,
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

type dashboardPageData struct {
	Metrics systemmetrics.Snapshot
	Error   string
}
