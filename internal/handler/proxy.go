package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"redlaunch/internal/application"
)

func (h *Handler) proxyPage(w http.ResponseWriter, r *http.Request) {
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

	details, err := h.proxyManager.GetProxyDetails(r.Context())
	if err != nil {
		h.logger.Error("get proxy details", "error", err)
		http.Error(w, "The proxy details could not be read.", http.StatusInternalServerError)
		return
	}
	h.writeProxyPage(w, r, http.StatusOK, proxyPageData{Details: details})
}

func (h *Handler) startProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "start")
}

func (h *Handler) stopProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "stop")
}

func (h *Handler) restartProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "restart")
}

func (h *Handler) proxyAction(w http.ResponseWriter, r *http.Request, action string) {
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

	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The proxy action request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This proxy action page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	switch action {
	case "start", "stop", "restart":
	default:
		http.NotFound(w, r)
		return
	}

	job, created, err := h.proxyActionJobs.createUnique(action)
	if err != nil {
		if errors.Is(err, ErrProxyActionBusy) {
			http.Error(w, "Another proxy operation is already running. Try again shortly.", http.StatusConflict)
			return
		}
		h.logger.Error("create proxy action job", "action", action, "error", err)
		http.Error(w, "The proxy operation could not be started.", http.StatusInternalServerError)
		return
	}
	if created {
		if err := h.startTrackedJob("proxy-action", func(ctx context.Context) {
			h.runProxyActionJob(ctx, job)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit proxy action job", "action", action, "error", err)
			http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}
	location := "/proxy?proxy_action_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) proxyActionStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The proxy operation job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.proxyActionProgress(jobID)
	if !ok {
		http.Error(w, "The proxy operation job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "proxy-action-toast.html", pageData{ProxyActionProgress: progress}, http.StatusOK)
}

func (h *Handler) proxyActionProgress(jobID string) (*proxyActionProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	job := h.proxyActionJobs.get(jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/proxy/action/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/proxy"
	return &progress, true
}

func (h *Handler) proxyActionToastsForPage(explicitJobID string) []*proxyActionProgressData {
	seen := make(map[string]struct{})
	var toasts []*proxyActionProgressData
	if explicitJobID != "" {
		if job := h.proxyActionJobs.get(explicitJobID); job != nil {
			snapshot := job.snapshot()
			snapshot.StatusURL = "/proxy/action/status?id=" + url.QueryEscape(snapshot.JobID)
			snapshot.CloseURL = "/proxy"
			toasts = append(toasts, &snapshot)
			seen[snapshot.JobID] = struct{}{}
		}
	}
	for _, job := range h.proxyActionJobs.active() {
		snapshot := job.snapshot()
		if _, ok := seen[snapshot.JobID]; ok {
			continue
		}
		snapshot.StatusURL = "/proxy/action/status?id=" + url.QueryEscape(snapshot.JobID)
		snapshot.CloseURL = "/proxy"
		toasts = append(toasts, &snapshot)
	}
	return toasts
}

func (h *Handler) downloadProxyLogs(w http.ResponseWriter, r *http.Request) {
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

	logsService, hasLegacyLogs := h.proxyManager.(proxyFullLogsService)
	streamer, hasStream := h.proxyManager.(proxyLogStreamService)
	if !hasLegacyLogs && !hasStream {
		http.Error(w, "Proxy log downloads are not configured.", http.StatusInternalServerError)
		return
	}
	release, err := h.acquireLogDownload(r.Context())
	if err != nil {
		http.Error(w, "Too many log downloads are active. Try again shortly.", http.StatusTooManyRequests)
		return
	}
	defer release()

	if hasStream {
		stream, err := streamer.OpenProxyLogs(r.Context())
		if err != nil {
			h.logger.Error("open full proxy log stream", "error", err)
			http.Error(w, "The proxy logs could not be read.", http.StatusInternalServerError)
			return
		}
		h.writeLogDownload(w, stream, "proxy-logs.txt", "proxy")
		return
	}

	logs, err := logsService.GetProxyFullLogs(r.Context())
	if err != nil {
		h.logger.Error("get full proxy logs", "error", err)
		http.Error(w, "The proxy logs could not be read.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="proxy-logs.txt"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(logs)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.WriteString(w, logs); err != nil {
		h.logger.Error("download full proxy logs", "error", err)
	}
}

func (h *Handler) writeProxyPage(w http.ResponseWriter, r *http.Request, status int, data proxyPageData) {
	csrfToken := h.setCSRFCookie(w, r)
	data.CSRFToken = csrfToken
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ActivePage = "proxy"
	page.ProxyPage = &data
	if h.proxyActionJobs != nil {
		page.ProxyActionToasts = h.proxyActionToastsForPage(r.URL.Query().Get("proxy_action_job"))
	}
	h.writeTemplateStatus(w, "proxy.html", page, status)
}

func proxyCreatedAtText(details application.ProxyDetails) string {
	if details.CreatedAt.IsZero() {
		return "—"
	}
	return details.CreatedAt.Format("2006-01-02 15:04")
}

func proxyCreatedAtISO(details application.ProxyDetails) string {
	if details.CreatedAt.IsZero() {
		return ""
	}
	return details.CreatedAt.Format(time.RFC3339)
}

func proxyCreatedAtTitle(details application.ProxyDetails) string {
	if details.CreatedAt.IsZero() {
		return "Creation date unavailable"
	}
	return details.CreatedAt.Format("2006-01-02 15:04:05 MST")
}
