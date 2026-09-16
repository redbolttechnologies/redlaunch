package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

func (h *Handler) createServerSSHKey(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The SSH key request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	displayName := r.Form.Get("display_name")
	serviceRef := strings.TrimSpace(r.Form.Get("service_ref"))
	if h.serverSSHKeys == nil {
		h.logger.Error("create server SSH key without a key service")
		h.renderSSHKeyError(w, r, displayName, serviceRef, nil, "The SSH key could not be created right now.", http.StatusInternalServerError)
		return
	}
	input := application.ServerSSHKeyInput{DisplayName: displayName}
	if serviceRef != "" {
		applicationID, serviceName, ok := parseSSHKeyServiceRef(serviceRef)
		if !ok {
			h.renderSSHKeyError(w, r, displayName, serviceRef, nil, sshKeyCreateMessage(application.ErrSSHKeyServiceRequired), http.StatusBadRequest)
			return
		}
		input.ApplicationID = applicationID
		input.ServiceName = serviceName
	}
	setup, err := h.serverSSHKeys.Create(r.Context(), input)
	if err != nil {
		if sshKeyCreateUserError(err) {
			h.renderSSHKeyError(w, r, displayName, serviceRef, nil, sshKeyCreateMessage(err), http.StatusBadRequest)
			return
		}
		h.logger.Error("create server SSH key", "error", err)
		h.renderSSHKeyError(w, r, displayName, serviceRef, nil, "The SSH key could not be created right now.", http.StatusInternalServerError)
		return
	}
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after SSH key creation", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.SSHKeySetup = &setup
	data.SSHActive = true
	h.writeSettingsPage(w, r, http.StatusCreated, data)
}

func (h *Handler) revokeServerSSHKey(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The SSH key revoke request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	rawID := r.Form.Get("id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 {
		h.renderSSHKeyDeleteError(w, r, 0, "", "The SSH key could not be found. Refresh the page and try again.", http.StatusBadRequest)
		return
	}
	if h.serverSSHKeys == nil {
		h.logger.Error("revoke server SSH key without a key service")
		h.renderSSHKeyDeleteError(w, r, id, "", "The SSH key could not be revoked right now.", http.StatusInternalServerError)
		return
	}
	displayName := sshKeyDisplayNameForDelete(r.Context(), h, id)
	if err := h.serverSSHKeys.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, application.ErrSSHKeyNotFound) {
			h.renderSSHKeyDeleteError(w, r, id, displayName, "The SSH key could not be found. Refresh the page and try again.", http.StatusNotFound)
			return
		}
		h.logger.Error("revoke server SSH key", "error", err)
		h.renderSSHKeyDeleteError(w, r, id, displayName, "The SSH key could not be revoked right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?tab=ssh-keys", http.StatusSeeOther)
}

func sshKeyDisplayNameForDelete(ctx context.Context, h *Handler, id int64) string {
	if h.serverSSHKeys == nil {
		return ""
	}
	keys, err := h.serverSSHKeys.List(ctx)
	if err != nil {
		return ""
	}
	for _, key := range keys {
		if key.ID == id {
			return key.DisplayName
		}
	}
	return ""
}

// parseSSHKeyServiceRef splits a restriction picker value of the form
// "<applicationID>/<service>". Service names cannot contain slashes, so the
// first slash is a safe separator.
func parseSSHKeyServiceRef(value string) (int64, string, bool) {
	applicationPart, servicePart, ok := strings.Cut(value, "/")
	if !ok {
		return 0, "", false
	}
	applicationID, err := strconv.ParseInt(strings.TrimSpace(applicationPart), 10, 64)
	if err != nil || applicationID < 1 || strings.TrimSpace(servicePart) == "" {
		return 0, "", false
	}
	return applicationID, strings.TrimSpace(servicePart), true
}

func (h *Handler) renderSSHKeyError(w http.ResponseWriter, r *http.Request, name, serviceRef string, deleteData *sshKeyDeletePageData, message string, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after SSH key failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.SSHKeyCreateOpen = true
	data.SSHKeyError = message
	data.SSHKeyName = name
	data.SSHKeyServiceRef = serviceRef
	data.SSHKeyDelete = deleteData
	data.SSHActive = true
	h.writeSettingsPage(w, r, status, data)
}

func (h *Handler) renderSSHKeyDeleteError(w http.ResponseWriter, r *http.Request, id int64, displayName, message string, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after SSH key revoke failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.SSHKeyDelete = &sshKeyDeletePageData{Open: true, ID: id, DisplayName: displayName, Error: message}
	data.SSHActive = true
	h.writeSettingsPage(w, r, status, data)
}

func sshKeyCreateUserError(err error) bool {
	return errors.Is(err, application.ErrSSHKeyDisplayNameRequired) ||
		errors.Is(err, application.ErrSSHKeyDisplayNameTooLong) ||
		errors.Is(err, application.ErrSSHKeyDisplayNameInvalid) ||
		errors.Is(err, application.ErrSSHKeyServiceRequired) ||
		errors.Is(err, application.ErrSSHKeyServiceNotFound) ||
		errors.Is(err, application.ErrSSHKeyServiceHasNoTargetPort)
}

func sshKeyCreateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrSSHKeyDisplayNameRequired):
		return "Enter a display name for the SSH key."
	case errors.Is(err, application.ErrSSHKeyDisplayNameTooLong):
		return "The display name is too long."
	case errors.Is(err, application.ErrSSHKeyDisplayNameInvalid):
		return "The display name contains invalid characters."
	case errors.Is(err, application.ErrSSHKeyServiceRequired):
		return "Select a service to restrict to, or leave the restriction empty for full shell access."
	case errors.Is(err, application.ErrSSHKeyServiceNotFound):
		return "The selected service could not be found. Refresh the page and try again."
	case errors.Is(err, application.ErrSSHKeyServiceHasNoTargetPort):
		return "The selected service publishes no host port, so it cannot be used as a tunnel restriction target."
	default:
		return "The SSH key could not be created."
	}
}
