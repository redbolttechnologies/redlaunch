package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

// apiTokenExpiryOptions are the lifetimes offered by the creation form, in
// days. The empty selection creates a token that never expires.
var apiTokenExpiryOptions = []int{30, 90, 180, 365}

func (h *Handler) createAPIToken(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The API token request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	displayName := r.Form.Get("display_name")
	applicationRef := strings.TrimSpace(r.Form.Get("application_id"))
	expiryRef := strings.TrimSpace(r.Form.Get("expires"))
	if h.apiTokens == nil {
		h.logger.Error("create API token without a token service")
		h.renderAPITokenError(w, r, displayName, applicationRef, expiryRef, nil, "The API token could not be created right now.", http.StatusInternalServerError)
		return
	}
	input, ok := parseAPITokenInput(displayName, applicationRef, expiryRef)
	if !ok {
		h.renderAPITokenError(w, r, displayName, applicationRef, expiryRef, nil, "Select an application and a valid expiry for the API token.", http.StatusBadRequest)
		return
	}
	setup, err := h.apiTokens.Create(r.Context(), input)
	if err != nil {
		if apiTokenCreateUserError(err) {
			h.renderAPITokenError(w, r, displayName, applicationRef, expiryRef, nil, apiTokenCreateMessage(err), http.StatusBadRequest)
			return
		}
		h.logger.Error("create API token", "error", err)
		h.renderAPITokenError(w, r, displayName, applicationRef, expiryRef, nil, "The API token could not be created right now.", http.StatusInternalServerError)
		return
	}
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after API token creation", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.APITokenSetup = &setup
	data.APITokensActive = true
	h.writeSettingsPage(w, r, http.StatusCreated, data)
}

// parseAPITokenInput validates the creation form selection. The application
// reference must be a positive ID and the expiry one of the offered options
// (or empty for a token that never expires).
func parseAPITokenInput(displayName, applicationRef, expiryRef string) (application.APITokenInput, bool) {
	applicationID, err := strconv.ParseInt(applicationRef, 10, 64)
	if err != nil || applicationID < 1 {
		return application.APITokenInput{}, false
	}
	expiresInDays := 0
	if expiryRef != "" && expiryRef != "never" {
		days, err := strconv.Atoi(expiryRef)
		if err != nil {
			return application.APITokenInput{}, false
		}
		allowed := false
		for _, option := range apiTokenExpiryOptions {
			if days == option {
				allowed = true
				break
			}
		}
		if !allowed {
			return application.APITokenInput{}, false
		}
		expiresInDays = days
	}
	return application.APITokenInput{
		DisplayName:   displayName,
		ApplicationID: applicationID,
		ExpiresInDays: expiresInDays,
	}, true
}

func (h *Handler) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The API token revoke request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	rawID := r.Form.Get("id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 {
		h.renderAPITokenDeleteError(w, r, 0, "", "The API token could not be found. Refresh the page and try again.", http.StatusBadRequest)
		return
	}
	if h.apiTokens == nil {
		h.logger.Error("revoke API token without a token service")
		h.renderAPITokenDeleteError(w, r, id, "", "The API token could not be revoked right now.", http.StatusInternalServerError)
		return
	}
	displayName := apiTokenDisplayNameForDelete(r.Context(), h, id)
	if err := h.apiTokens.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, application.ErrAPITokenNotFound) {
			h.renderAPITokenDeleteError(w, r, id, displayName, "The API token could not be found. Refresh the page and try again.", http.StatusNotFound)
			return
		}
		h.logger.Error("revoke API token", "error", err)
		h.renderAPITokenDeleteError(w, r, id, displayName, "The API token could not be revoked right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?tab=api-tokens", http.StatusSeeOther)
}

func apiTokenDisplayNameForDelete(ctx context.Context, h *Handler, id int64) string {
	if h.apiTokens == nil {
		return ""
	}
	tokens, err := h.apiTokens.List(ctx)
	if err != nil {
		return ""
	}
	for _, token := range tokens {
		if token.ID == id {
			return token.DisplayName
		}
	}
	return ""
}

func (h *Handler) renderAPITokenError(w http.ResponseWriter, r *http.Request, name, applicationRef, expiryRef string, deleteData *apiTokenDeletePageData, message string, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after API token failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.APITokenCreateOpen = true
	data.APITokenError = message
	data.APITokenName = name
	data.APITokenApplicationRef = applicationRef
	data.APITokenExpiryRef = expiryRef
	data.APITokenDelete = deleteData
	data.APITokensActive = true
	h.writeSettingsPage(w, r, status, data)
}

func (h *Handler) renderAPITokenDeleteError(w http.ResponseWriter, r *http.Request, id int64, displayName, message string, status int) {
	data, err := h.loadSettingsPageData(r.Context())
	if err != nil {
		h.logger.Error("load settings page after API token revoke failure", "error", err)
		http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
		return
	}
	data.APITokenDelete = &apiTokenDeletePageData{Open: true, ID: id, DisplayName: displayName, Error: message}
	data.APITokensActive = true
	h.writeSettingsPage(w, r, status, data)
}

func apiTokenCreateUserError(err error) bool {
	return errors.Is(err, application.ErrAPITokenDisplayNameRequired) ||
		errors.Is(err, application.ErrAPITokenDisplayNameTooLong) ||
		errors.Is(err, application.ErrAPITokenDisplayNameInvalid) ||
		errors.Is(err, application.ErrAPITokenApplicationRequired) ||
		errors.Is(err, application.ErrAPITokenApplicationNotFound) ||
		errors.Is(err, application.ErrAPITokenExpiryInvalid)
}

func apiTokenCreateMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrAPITokenDisplayNameRequired):
		return "Enter a display name for the API token."
	case errors.Is(err, application.ErrAPITokenDisplayNameTooLong):
		return "The display name is too long."
	case errors.Is(err, application.ErrAPITokenDisplayNameInvalid):
		return "The display name contains invalid characters."
	case errors.Is(err, application.ErrAPITokenApplicationRequired),
		errors.Is(err, application.ErrAPITokenApplicationNotFound):
		return "Select an application for the API token. Refresh the page and try again."
	case errors.Is(err, application.ErrAPITokenExpiryInvalid):
		return "Select a valid expiry for the API token."
	default:
		return "The API token could not be created."
	}
}
