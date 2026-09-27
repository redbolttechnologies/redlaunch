package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"redlaunch/internal/application"
)

// applicationRenamer renames one application's display name. The production
// service implements it; handlers treat a missing implementation as
// unconfigured.
type applicationRenamer interface {
	RenameApplication(ctx context.Context, id int64, name string) (application.Application, error)
}

// applicationNameEditData carries the inline title-edit state for the
// application details page. A nil value renders the static title; a non-nil
// value with Editing renders the form open with the submitted value and an
// inline validation message.
type applicationNameEditData struct {
	Editing bool
	Value   string
	Error   string
}

func (h *Handler) renameApplication(w http.ResponseWriter, r *http.Request) {
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

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if h.applicationRenamer == nil {
		h.logger.Error("rename application without a renamer", "application_id", id)
		http.Error(w, "The application rename is not configured.", http.StatusInternalServerError)
		return
	}

	if err := parseBoundedForm(w, r); err != nil {
		h.renderApplicationNameError(w, r, id, "The rename request was invalid.", firstFormValue(r.Form, "name"), http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This application page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	name := firstFormValue(r.Form, "name")
	renamed, err := h.applicationRenamer.RenameApplication(r.Context(), id, name)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if renameUserError(err) {
			h.logger.Info("rename application rejected", "application_id", id, "reason", renameUserMessage(err))
			h.renderApplicationNameError(w, r, id, renameUserMessage(err), name, http.StatusBadRequest)
			return
		}
		h.logger.Error("rename application", "application_id", id, "error", err)
		if isApplicationNameFragmentRequest(r) {
			http.Error(w, "The application could not be renamed right now.", http.StatusInternalServerError)
			return
		}
		h.renderApplicationNameError(w, r, id, "The application could not be renamed right now.", name, http.StatusInternalServerError)
		return
	}

	if isApplicationNameFragmentRequest(r) {
		h.writeApplicationNameFragment(w, r, http.StatusOK, renamed, nil)
		return
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// renderApplicationNameError re-renders the title editor with an inline
// validation message. HTMX requests receive the title fragment with status
// 422 so the swap replaces the title block; plain form submissions re-render
// the full details page so the form keeps working without JavaScript.
func (h *Handler) renderApplicationNameError(w http.ResponseWriter, r *http.Request, id int64, message, value string, status int) {
	edit := &applicationNameEditData{Editing: true, Value: value, Error: message}
	if isApplicationNameFragmentRequest(r) {
		item, err := h.applicationDetails.Get(r.Context(), id)
		if errors.Is(err, application.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.logger.Error("load application after rename failure", "application_id", id, "error", err)
			http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
			return
		}
		h.writeApplicationNameFragment(w, r, http.StatusUnprocessableEntity, item, edit)
		return
	}
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after rename failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.NameEdit = edit
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

func (h *Handler) writeApplicationNameFragment(w http.ResponseWriter, r *http.Request, status int, item application.Application, edit *applicationNameEditData) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := pageData{ApplicationDetailsPage: &applicationDetailsPageData{
		Application: item,
		CSRFToken:   h.csrfTokenForRequest(r),
		NameEdit:    edit,
	}}
	if err := h.templates.ExecuteTemplate(w, "application-name-fragment", data); err != nil {
		h.logger.Error("render application name fragment", "error", err)
	}
}

// applicationNameEditValue resolves the title input value: the submitted
// value while the editor is open with an error, otherwise the stored name.
func applicationNameEditValue(page *applicationDetailsPageData) string {
	if page != nil && page.NameEdit != nil && page.NameEdit.Editing {
		return page.NameEdit.Value
	}
	if page != nil {
		return page.Application.Name
	}
	return ""
}

// isApplicationNameFragmentRequest reports whether the rename came from the
// inline HTMX editor rather than a plain form submission.
func isApplicationNameFragmentRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func renameUserError(err error) bool {
	return errors.Is(err, application.ErrNameRequired) ||
		errors.Is(err, application.ErrNameTooLong) ||
		errors.Is(err, application.ErrNameInvalid) ||
		errors.Is(err, application.ErrAlreadyExists)
}

func renameUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrNameRequired):
		return "Enter an application name."
	case errors.Is(err, application.ErrNameTooLong):
		return "Application names must be 64 characters or fewer."
	case errors.Is(err, application.ErrNameInvalid):
		return "Application names cannot contain control characters."
	case errors.Is(err, application.ErrAlreadyExists):
		return "An application with that name already exists."
	default:
		return "The application could not be renamed right now."
	}
}
