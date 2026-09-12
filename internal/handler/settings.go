package handler

import (
	"context"
	"errors"
	"net/http"

	"redlaunch/internal/application"
)

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
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

	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	h.writeSettingsPage(w, r, http.StatusOK, data)
}

func (h *Handler) loadSettingsPageData(ctx context.Context) (settingsPageData, error) {
	if h.redlaunchDomains == nil {
		return settingsPageData{}, errors.New("Redlaunch domain service is not configured")
	}
	domains, err := h.redlaunchDomains.ListRedlaunchDomains(ctx)
	if err != nil {
		return settingsPageData{}, err
	}
	return settingsPageData{Domains: domains}, nil
}

func (h *Handler) createRedlaunchDomain(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The domain save request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	edit := &domainEditPageData{
		Open: true,
		Name: r.Form.Get("name"),
	}
	if h.redlaunchDomains == nil {
		h.logger.Error("create Redlaunch domain without a domain service")
		edit.Error = "The domain could not be saved right now."
		h.renderSettingsDomainEditError(w, r, edit, http.StatusInternalServerError)
		return
	}
	if _, err := h.redlaunchDomains.CreateRedlaunchDomain(r.Context(), edit.Name); err != nil {
		if redlaunchDomainCreateUserError(err) {
			edit.Error = redlaunchDomainCreateMessage(err)
			h.renderSettingsDomainEditError(w, r, edit, http.StatusBadRequest)
			return
		}
		h.logger.Error("create Redlaunch domain", "error", err)
		edit.Error = "The domain could not be saved right now."
		h.renderSettingsDomainEditError(w, r, edit, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *Handler) deleteRedlaunchDomain(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The domain delete request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	name := r.Form.Get("name")
	if h.redlaunchDomains == nil {
		h.logger.Error("delete Redlaunch domain without a domain service")
		h.renderSettingsDomainDeleteError(w, r, &domainDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The domain could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	if err := h.redlaunchDomains.DeleteRedlaunchDomain(r.Context(), name); err != nil {
		if redlaunchDomainDeleteUserError(err) {
			h.renderSettingsDomainDeleteError(w, r, &domainDeletePageData{
				Open:  true,
				Name:  name,
				Error: redlaunchDomainDeleteMessage(err),
			}, http.StatusBadRequest)
			return
		}
		h.logger.Error("delete Redlaunch domain", "error", err)
		h.renderSettingsDomainDeleteError(w, r, &domainDeletePageData{
			Open:  true,
			Name:  name,
			Error: "The domain could not be deleted right now.",
		}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *Handler) renderSettingsDomainEditError(w http.ResponseWriter, r *http.Request, edit *domainEditPageData, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after domain create failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.DomainEdit = edit
	h.writeSettingsPage(w, r, status, data)
}

func (h *Handler) renderSettingsDomainDeleteError(w http.ResponseWriter, r *http.Request, deleteData *domainDeletePageData, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after domain delete failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.DomainDelete = deleteData
	h.writeSettingsPage(w, r, status, data)
}

func redlaunchDomainCreateUserError(err error) bool {
	return errors.Is(err, application.ErrDomainNameRequired) ||
		errors.Is(err, application.ErrDomainNameTooLong) ||
		errors.Is(err, application.ErrDomainNameInvalid) ||
		errors.Is(err, application.ErrDomainAlreadyExists)
}

func redlaunchDomainCreateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrDomainNameRequired):
		return "Enter a domain name."
	case errors.Is(err, application.ErrDomainNameTooLong):
		return "The domain name is too long."
	case errors.Is(err, application.ErrDomainNameInvalid):
		return "Enter a valid domain name."
	case errors.Is(err, application.ErrDomainAlreadyExists):
		return "This domain is already configured for Redlaunch."
	default:
		return "The domain could not be saved."
	}
}

func redlaunchDomainDeleteUserError(err error) bool {
	return errors.Is(err, application.ErrDomainNameRequired) ||
		errors.Is(err, application.ErrDomainNameTooLong) ||
		errors.Is(err, application.ErrDomainNameInvalid) ||
		errors.Is(err, application.ErrDomainNotFound)
}

func redlaunchDomainDeleteMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrDomainNameRequired):
		return "The domain name is required."
	case errors.Is(err, application.ErrDomainNameTooLong), errors.Is(err, application.ErrDomainNameInvalid):
		return "The domain name is invalid."
	case errors.Is(err, application.ErrDomainNotFound):
		return "The domain could not be found. Refresh the page and try again."
	default:
		return "The domain could not be deleted."
	}
}

func (h *Handler) writeSettingsPage(w http.ResponseWriter, r *http.Request, status int, data settingsPageData) {
	csrfToken := h.setCSRFCookie(w, r)
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "settings"
	page.SettingsPage = &data
	h.writeTemplateStatus(w, "settings.html", page, status)
}
