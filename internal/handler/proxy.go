package handler

import (
	"errors"
	"io"
	"net/http"
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

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The proxy action request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This proxy action page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	var actionErr error
	switch action {
	case "start":
		actionErr = h.proxyActions.StartProxy(r.Context())
	case "stop":
		actionErr = h.proxyActions.StopProxy(r.Context())
	case "restart":
		actionErr = h.proxyActions.RestartProxy(r.Context())
	default:
		http.NotFound(w, r)
		return
	}
	if actionErr != nil {
		if errors.Is(actionErr, application.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.logger.Error("run proxy action", "action", action, "error", actionErr)
		http.Error(w, "The proxy could not be "+serviceActionPastTense(action)+".", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/proxy", http.StatusSeeOther)
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
