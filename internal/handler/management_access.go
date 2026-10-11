package handler

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) saveManagementAccess(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The access mode request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Refresh it and try again.", http.StatusForbidden)
		return
	}
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	mode := r.Form.Get("access_mode")
	if mode == accessModeSSHOnly || mode == accessModeManagedHTTPS {
		data.ManagementAccessSelection = mode
	}
	status := http.StatusBadRequest
	switch {
	case mode != accessModeSSHOnly && mode != accessModeManagedHTTPS:
		data.ManagementAccessError = "Choose SSH-only or Managed HTTPS."
	case r.Form.Get("confirm") != "yes":
		data.ManagementAccessError = "Confirm that you understand the restart and access requirements."
	case r.Form.Get("current_mode") != h.accessMode:
		data.ManagementAccessError = "The running mode changed. Refresh Settings before trying again."
		status = http.StatusConflict
	case mode == accessModeManagedHTTPS && len(data.Domains) == 0:
		data.ManagementAccessError = "Add a Redlaunch domain before enabling Managed HTTPS."
	default:
		manager, ok := h.selfUpdater.(managementAccessService)
		if !ok {
			data.ManagementAccessError = "The installation restart service is not configured."
			status = http.StatusServiceUnavailable
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
			err := manager.QueueManagementAccessMode(ctx, mode)
			cancel()
			if err != nil {
				h.logger.Error("save management access mode", "error", err)
				data.ManagementAccessError = "The restart could not be confirmed. Check the server logs and installation .env before trying again."
				status = http.StatusInternalServerError
			} else {
				data.ManagementAccessQueued = true
				status = http.StatusOK
			}
		}
	}
	h.writeSettingsPage(w, r, status, data)
}
