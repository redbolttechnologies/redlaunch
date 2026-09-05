package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"redlaunch/internal/application"
)

func (h *Handler) applicationRoutingPage(w http.ResponseWriter, r *http.Request) {
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

	applicationID, domainID, ok := parseApplicationRoutingPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := h.loadApplicationRoutingPageData(r.Context(), applicationID, domainID)
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrDomainNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application routing page", "application_id", applicationID, "domain_id", domainID, "error", err)
		http.Error(w, "The application routing page could not be read.", http.StatusInternalServerError)
		return
	}
	h.writeApplicationRoutingPage(w, r, http.StatusOK, data)
}

func (h *Handler) loadApplicationRoutingPageData(ctx context.Context, applicationID, domainID int64) (applicationRoutingPageData, error) {
	if h.applicationRoutings == nil {
		return applicationRoutingPageData{}, errors.New("application routing service is not configured")
	}
	item, err := h.applicationDetails.Get(ctx, applicationID)
	if err != nil {
		return applicationRoutingPageData{}, err
	}
	domains, err := h.applicationDomains.ListDomains(ctx, applicationID)
	if err != nil {
		return applicationRoutingPageData{}, fmt.Errorf("list application domains: %w", err)
	}
	var domain application.Domain
	for _, candidate := range domains {
		if candidate.ID == domainID {
			domain = candidate
			break
		}
	}
	if domain.ID == 0 {
		return applicationRoutingPageData{}, application.ErrDomainNotFound
	}
	services, err := h.applicationDetails.ListServices(ctx, applicationID)
	if err != nil {
		return applicationRoutingPageData{}, fmt.Errorf("list application services for routing: %w", err)
	}
	routings, err := h.applicationRoutings.ListRoutings(ctx, applicationID, domainID)
	if err != nil {
		return applicationRoutingPageData{}, fmt.Errorf("list application routings: %w", err)
	}
	return applicationRoutingPageData{
		Application: item,
		Domain:      domain,
		Routings:    routings,
		Services:    services,
	}, nil
}

func (h *Handler) saveApplicationRouting(w http.ResponseWriter, r *http.Request) {
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

	applicationID, domainID, ok := parseApplicationRoutingPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The routing save request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This routing page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	edit := &routingEditPageData{
		Open:        true,
		Add:         r.Form.Get("operation") != "edit",
		Subdomain:   r.Form.Get("subdomain"),
		Path:        r.Form.Get("path"),
		ServiceName: firstFormValue(r.Form, "service", "service_name"),
		ServicePath: firstFormValue(r.Form, "service_path", "target_path"),
	}
	routingID, err := parseRoutingID(r.Form.Get("routing_id"))
	if r.Form.Get("operation") == "edit" {
		if err != nil {
			renderRoutingError := &routingEditPageData{
				Open:        true,
				Add:         false,
				Error:       "The routing could not be identified. Refresh the page and try again.",
				Subdomain:   edit.Subdomain,
				Path:        edit.Path,
				ServiceName: edit.ServiceName,
				ServicePath: edit.ServicePath,
			}
			h.renderApplicationRoutingEditError(w, r, applicationID, domainID, renderRoutingError, http.StatusBadRequest)
			return
		}
		edit.ID = routingID
	}

	input := application.RoutingInput{
		Subdomain:   edit.Subdomain,
		Path:        edit.Path,
		ServiceName: edit.ServiceName,
		ServicePath: edit.ServicePath,
	}
	if h.applicationRoutings == nil {
		edit.Error = "The routing could not be saved right now."
		h.renderApplicationRoutingEditError(w, r, applicationID, domainID, edit, http.StatusInternalServerError)
		return
	}
	if edit.Add {
		if _, err := h.applicationRoutings.CreateRouting(r.Context(), applicationID, domainID, input); err != nil {
			if routingSaveUserError(err) {
				edit.Error = routingSaveMessage(err)
				h.renderApplicationRoutingEditError(w, r, applicationID, domainID, edit, http.StatusBadRequest)
				return
			}
			h.logger.Error("create application routing", "application_id", applicationID, "domain_id", domainID, "error", err)
			edit.Error = "The routing could not be saved right now."
			h.renderApplicationRoutingEditError(w, r, applicationID, domainID, edit, http.StatusInternalServerError)
			return
		}
	} else if err := h.applicationRoutings.UpdateRouting(r.Context(), applicationID, domainID, edit.ID, input); err != nil {
		if routingSaveUserError(err) {
			edit.Error = routingSaveMessage(err)
			h.renderApplicationRoutingEditError(w, r, applicationID, domainID, edit, http.StatusBadRequest)
			return
		}
		h.logger.Error("update application routing", "application_id", applicationID, "domain_id", domainID, "routing_id", edit.ID, "error", err)
		edit.Error = "The routing could not be saved right now."
		h.renderApplicationRoutingEditError(w, r, applicationID, domainID, edit, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, applicationRoutingPath(applicationID, domainID), http.StatusSeeOther)
}

func (h *Handler) deleteApplicationRouting(w http.ResponseWriter, r *http.Request) {
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

	applicationID, domainID, ok := parseApplicationRoutingPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The routing delete request was invalid.", http.StatusBadRequest)
		return
	}
	expectedCSRFToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expectedCSRFToken = cookie.Value
	}
	if !validCSRFToken(r.Form.Get("csrf_token"), expectedCSRFToken) {
		http.Error(w, "This routing page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	routingID, err := parseRoutingID(firstFormValue(r.Form, "routing_id", "id"))
	deleteData := &routingDeletePageData{
		Open: true,
		ID:   routingID,
		Host: r.Form.Get("host"),
	}
	if err != nil {
		deleteData.Error = "The routing could not be identified. Refresh the page and try again."
		h.renderApplicationRoutingDeleteError(w, r, applicationID, domainID, deleteData, http.StatusBadRequest)
		return
	}
	if h.applicationRoutings == nil {
		deleteData.Error = "The routing could not be deleted right now."
		h.renderApplicationRoutingDeleteError(w, r, applicationID, domainID, deleteData, http.StatusInternalServerError)
		return
	}
	if err := h.applicationRoutings.DeleteRouting(r.Context(), applicationID, domainID, routingID); err != nil {
		if errors.Is(err, application.ErrRoutingNotFound) {
			deleteData.Error = "The routing could not be found. Refresh the page and try again."
			h.renderApplicationRoutingDeleteError(w, r, applicationID, domainID, deleteData, http.StatusBadRequest)
			return
		}
		h.logger.Error("delete application routing", "application_id", applicationID, "domain_id", domainID, "routing_id", routingID, "error", err)
		deleteData.Error = "The routing could not be deleted right now."
		h.renderApplicationRoutingDeleteError(w, r, applicationID, domainID, deleteData, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, applicationRoutingPath(applicationID, domainID), http.StatusSeeOther)
}

func parseApplicationRoutingPath(r *http.Request) (int64, int64, bool) {
	applicationID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || applicationID < 1 {
		return 0, 0, false
	}
	domainID, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil || domainID < 1 {
		return 0, 0, false
	}
	return applicationID, domainID, true
}

func parseRoutingID(value string) (int64, error) {
	routingID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || routingID < 1 {
		return 0, errors.New("routing ID must be positive")
	}
	return routingID, nil
}

func routingSaveUserError(err error) bool {
	return errors.Is(err, application.ErrRoutingSubdomainTooLong) ||
		errors.Is(err, application.ErrRoutingSubdomainInvalid) ||
		errors.Is(err, application.ErrRoutingHostTooLong) ||
		errors.Is(err, application.ErrRoutingPathRequired) ||
		errors.Is(err, application.ErrRoutingPathTooLong) ||
		errors.Is(err, application.ErrRoutingPathInvalid) ||
		errors.Is(err, application.ErrServiceNameRequired) ||
		errors.Is(err, application.ErrServiceNameTooLong) ||
		errors.Is(err, application.ErrServiceNameInvalid) ||
		errors.Is(err, application.ErrServiceNotFound) ||
		errors.Is(err, application.ErrRoutingAlreadyExists) ||
		errors.Is(err, application.ErrRoutingNotFound)
}

func routingSaveMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrRoutingSubdomainTooLong):
		return "The subdomain is too long."
	case errors.Is(err, application.ErrRoutingSubdomainInvalid):
		return "Enter a valid subdomain using letters, numbers, hyphens, or dots."
	case errors.Is(err, application.ErrRoutingHostTooLong):
		return "The complete routing domain is too long."
	case errors.Is(err, application.ErrRoutingPathRequired):
		return "Enter both request and service paths."
	case errors.Is(err, application.ErrRoutingPathTooLong):
		return "Paths must be 2048 characters or fewer."
	case errors.Is(err, application.ErrRoutingPathInvalid):
		return "Paths must start with / and contain no spaces or control characters."
	case errors.Is(err, application.ErrServiceNameRequired), errors.Is(err, application.ErrServiceNotFound):
		return "Select a service to route."
	case errors.Is(err, application.ErrServiceNameTooLong), errors.Is(err, application.ErrServiceNameInvalid):
		return "The selected service is invalid."
	case errors.Is(err, application.ErrRoutingAlreadyExists):
		return "A routing with that subdomain and request path already exists."
	case errors.Is(err, application.ErrRoutingNotFound):
		return "The routing could not be found. Refresh the page and try again."
	default:
		return "The routing could not be saved."
	}
}

func (h *Handler) renderApplicationRoutingEditError(w http.ResponseWriter, r *http.Request, applicationID, domainID int64, edit *routingEditPageData, status int) {
	data, err := h.loadApplicationRoutingPageData(r.Context(), applicationID, domainID)
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrDomainNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application routing page after save failure", "application_id", applicationID, "domain_id", domainID, "error", err)
		http.Error(w, "The application routing page could not be read.", http.StatusInternalServerError)
		return
	}
	data.RoutingEdit = edit
	h.writeApplicationRoutingPage(w, r, status, data)
}

func (h *Handler) renderApplicationRoutingDeleteError(w http.ResponseWriter, r *http.Request, applicationID, domainID int64, deleteData *routingDeletePageData, status int) {
	data, err := h.loadApplicationRoutingPageData(r.Context(), applicationID, domainID)
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrDomainNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application routing page after delete failure", "application_id", applicationID, "domain_id", domainID, "error", err)
		http.Error(w, "The application routing page could not be read.", http.StatusInternalServerError)
		return
	}
	data.RoutingDelete = deleteData
	h.writeApplicationRoutingPage(w, r, status, data)
}

func (h *Handler) writeApplicationRoutingPage(w http.ResponseWriter, r *http.Request, status int, data applicationRoutingPageData) {
	csrfToken := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		csrfToken = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = csrfToken
	page := h.shellPageData(r)
	page.ActivePage = "applications"
	page.ApplicationRoutingPage = &data
	h.writeTemplateStatus(w, "application-routing.html", page, status)
}
