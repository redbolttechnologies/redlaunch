package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

// applicationCloner clones one application into a new, independent
// application. The production service implements it; handlers treat a missing
// implementation as unconfigured.
type applicationCloner interface {
	CloneApplication(ctx context.Context, sourceID int64, input application.CloneInput) (application.Application, error)
}

// cloneApplicationPageData carries the clone dialog state for the application
// details page. Error re-renders the Settings tab with the dialog open;
// ClonedFrom marks a fresh clone so the page can warn about copied secrets.
type cloneApplicationPageData struct {
	Open           bool
	Error          string
	Name           string
	FolderName     string
	CopyExtraFiles bool
}

func (h *Handler) cloneApplication(w http.ResponseWriter, r *http.Request) {
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
	if h.applicationCloner == nil {
		h.logger.Error("clone application without a cloner", "application_id", id)
		http.Error(w, "The application clone is not configured.", http.StatusInternalServerError)
		return
	}

	if err := parseBoundedForm(w, r); err != nil {
		h.renderApplicationCloneError(w, r, id, &cloneApplicationPageData{
			Open:           true,
			Error:          "The clone request was invalid.",
			Name:           firstFormValue(r.Form, "name", "application_name"),
			FolderName:     firstFormValue(r.Form, "folder_name", "folder"),
			CopyExtraFiles: firstFormValue(r.Form, "copy_extra_files") == "on",
		}, http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This application page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	input := application.CloneInput{
		Name:           firstFormValue(r.Form, "name", "application_name"),
		FolderName:     firstFormValue(r.Form, "folder_name", "folder"),
		CopyExtraFiles: firstFormValue(r.Form, "copy_extra_files") == "on",
	}
	cloned, err := h.applicationCloner.CloneApplication(r.Context(), id, input)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if cloneUserError(err) {
			h.logger.Info("clone application rejected", "application_id", id, "reason", cloneUserMessage(err))
			h.renderApplicationCloneError(w, r, id, &cloneApplicationPageData{
				Open:           true,
				Error:          cloneUserMessage(err),
				Name:           input.Name,
				FolderName:     input.FolderName,
				CopyExtraFiles: input.CopyExtraFiles,
			}, http.StatusBadRequest)
			return
		}
		h.logger.Error("clone application", "application_id", id, "error", err)
		h.renderApplicationCloneError(w, r, id, &cloneApplicationPageData{
			Open:           true,
			Error:          "The application could not be cloned right now.",
			Name:           input.Name,
			FolderName:     input.FolderName,
			CopyExtraFiles: input.CopyExtraFiles,
		}, http.StatusInternalServerError)
		return
	}
	location := "/applications/" + strconv.FormatInt(cloned.ID, 10) + "?cloned_from=" + url.QueryEscape(strconv.FormatInt(id, 10))
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) renderApplicationCloneError(w http.ResponseWriter, r *http.Request, id int64, clone *cloneApplicationPageData, status int) {
	data, err := h.loadApplicationDetailsPageData(r.Context(), id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load application details after clone failure", "application_id", id, "error", err)
		http.Error(w, "The application details could not be read.", http.StatusInternalServerError)
		return
	}
	data.Clone = clone
	h.writeApplicationDetailsPage(w, r, status, data, nil, nil)
}

// clonedFromSourceID returns the source application ID from a fresh clone
// redirect (?cloned_from=<id>). Zero means the page was not reached through
// a clone and no banner is shown.
func clonedFromSourceID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get("cloned_from"))
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

func cloneUserError(err error) bool {
	return errors.Is(err, application.ErrNameRequired) ||
		errors.Is(err, application.ErrNameTooLong) ||
		errors.Is(err, application.ErrNameInvalid) ||
		errors.Is(err, application.ErrFolderNameRequired) ||
		errors.Is(err, application.ErrFolderNameTooLong) ||
		errors.Is(err, application.ErrFolderNameInvalid) ||
		errors.Is(err, application.ErrAlreadyExists) ||
		errors.Is(err, application.ErrApplicationDeletionInProgress) ||
		errors.Is(err, application.ErrDatabaseCredentialsAmbiguous) ||
		errors.Is(err, application.ErrEnvironmentFileNotFound)
}

func cloneUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrNameRequired):
		return "Enter an application name."
	case errors.Is(err, application.ErrNameTooLong):
		return "Application names must be 64 characters or fewer."
	case errors.Is(err, application.ErrNameInvalid):
		return "Application names cannot contain control characters."
	case errors.Is(err, application.ErrFolderNameRequired):
		return "Enter a folder name."
	case errors.Is(err, application.ErrFolderNameTooLong):
		return "Folder names must be 64 characters or fewer."
	case errors.Is(err, application.ErrFolderNameInvalid):
		return "Folder names must be a single, valid directory name."
	case errors.Is(err, application.ErrAlreadyExists):
		return "An application with that name or folder already exists."
	case errors.Is(err, application.ErrApplicationDeletionInProgress):
		return "An application deletion is still in progress. Retry after deletion finishes."
	case errors.Is(err, application.ErrDatabaseCredentialsAmbiguous):
		return "The source application has incomplete scoped environment files. Fix them before cloning."
	case errors.Is(err, application.ErrEnvironmentFileNotFound):
		return "The source application is missing its environment files and cannot be cloned."
	default:
		return "The application could not be cloned right now."
	}
}
