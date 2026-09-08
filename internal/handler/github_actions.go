package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

const maxGitHubActionsFormBody = 64 << 10

type githubActionsPageData struct {
	Application  application.Application
	Services     []application.Service
	Integration  *application.GitHubActionsIntegration
	Setup        *application.GitHubActionsSetup
	Progress     *githubActionsProgressData
	CSRFToken    string
	Error        string
	Notice       string
	Repository   string
	Branch       string
	Dockerfile   string
	BuildContext string
	ServiceName  string
	ImageName    string
	ServerHost   string
}

func (h *Handler) githubActionsPage(w http.ResponseWriter, r *http.Request) {
	if !h.githubActionsSetupReady(w, r) {
		return
	}
	id, ok := parseApplicationID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	progress, progressOK := h.githubActionsProgress(id, r.URL.Query().Get("github_actions_job"))
	if !progressOK {
		http.Redirect(w, r, fmt.Sprintf("/applications/%d/deployments/github-actions", id), http.StatusSeeOther)
		return
	}
	data, err := h.githubActionsPageData(r, id)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("load GitHub Actions page", "application_id", id, "error", err)
		http.Error(w, "The GitHub Actions setup could not be loaded.", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("revoked") == "1" {
		data.Notice = "The GitHub Actions key was revoked."
	}
	if progress != nil {
		if r.URL.Query().Get("github_actions_handoff") == "1" && progress.State == githubActionsJobStateComplete {
			if job := h.githubActionsJobs.get(id, progress.JobID); job != nil {
				if setup, ok := job.setup(); ok {
					if data.Integration == nil || data.Integration.PublicKey != setup.Integration.PublicKey {
						data.Error = "This GitHub Actions handoff is no longer active. Run setup again to generate a current key."
					} else {
						data.Setup = &setup
						data.Integration = &setup.Integration
						data.Repository = setup.Integration.Repository
						data.Branch = setup.Integration.Branch
						data.Dockerfile = setup.Integration.Dockerfile
						data.BuildContext = setup.Integration.BuildContext
						data.ServiceName = setup.Integration.ServiceName
						data.ImageName = setup.Integration.ImageName
						data.ServerHost = setup.Integration.ServerHost
					}
				} else {
					data.Error = "This GitHub Actions handoff has expired. Run setup again to generate a new key."
				}
			}
			data.Progress = nil
		} else {
			data.Progress = progress
		}
	}
	h.writeGitHubActionsPage(w, r, http.StatusOK, data)
}

func (h *Handler) configureGitHubActions(w http.ResponseWriter, r *http.Request) {
	if !h.githubActionsSetupReady(w, r) {
		return
	}
	id, ok := parseApplicationID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGitHubActionsFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The GitHub Actions setup request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		data, err := h.githubActionsPageData(r, id)
		if err != nil {
			http.Error(w, "The GitHub Actions setup page could not be loaded.", http.StatusInternalServerError)
			return
		}
		data.Error = "This GitHub Actions setup page expired. Submit the refreshed form to continue."
		h.writeGitHubActionsPage(w, r, http.StatusForbidden, data)
		return
	}
	input := application.GitHubActionsInput{
		Repository:   r.Form.Get("repository"),
		Branch:       r.Form.Get("branch"),
		Dockerfile:   r.Form.Get("dockerfile"),
		BuildContext: r.Form.Get("build_context"),
		ServiceName:  r.Form.Get("service_name"),
		ImageName:    r.Form.Get("image_name"),
		ServerHost:   r.Form.Get("server_host"),
	}
	job, created, err := h.githubActionsJobs.create(id, githubActionsJobOperationConfigure)
	if err != nil {
		h.logger.Error("create GitHub Actions setup job", "application_id", id, "error", err)
		http.Error(w, "The GitHub Actions setup job could not be created.", http.StatusInternalServerError)
		return
	}
	if created {
		h.startGitHubActionsConfigureJob(job, input)
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d/deployments/github-actions?github_actions_job=%s", id, url.QueryEscape(job.id)), http.StatusSeeOther)
}

func (h *Handler) revokeGitHubActions(w http.ResponseWriter, r *http.Request) {
	if !h.githubActionsSetupReady(w, r) {
		return
	}
	id, ok := parseApplicationID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGitHubActionsFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "The GitHub Actions revoke request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This GitHub Actions page expired. Submit the refreshed form to continue.", http.StatusForbidden)
		return
	}
	if _, err := h.githubActions.Get(r.Context(), id); errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.logger.Error("load GitHub Actions integration for revocation", "application_id", id, "error", err)
		http.Error(w, "The GitHub Actions integration could not be loaded.", http.StatusInternalServerError)
		return
	}
	job, created, err := h.githubActionsJobs.create(id, githubActionsJobOperationRevoke)
	if err != nil {
		h.logger.Error("create GitHub Actions revoke job", "application_id", id, "error", err)
		http.Error(w, "The GitHub Actions revoke job could not be created.", http.StatusInternalServerError)
		return
	}
	if created {
		h.startGitHubActionsRevokeJob(job)
	}
	http.Redirect(w, r, fmt.Sprintf("/applications/%d/deployments/github-actions?github_actions_job=%s", id, url.QueryEscape(job.id)), http.StatusSeeOther)
}

func (h *Handler) githubActionsStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The GitHub Actions setup job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.githubActionsProgress(id, jobID)
	if !ok {
		http.Error(w, "The GitHub Actions setup job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "github-actions-progress.html", pageData{GitHubActionsProgress: progress}, http.StatusOK)
}

func (h *Handler) downloadGitHubActionsWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.githubActionsSetupReady(w, r) {
		return
	}
	id, ok := parseApplicationID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	workflow, err := h.githubActions.RenderWorkflow(r.Context(), id)
	if errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("render GitHub Actions workflow", "application_id", id, "error", err)
		http.Error(w, "The GitHub Actions workflow could not be rendered.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="redlaunch-push-image.yml"`)
	_, _ = w.Write([]byte(workflow))
}

func (h *Handler) githubActionsSetupReady(w http.ResponseWriter, r *http.Request) bool {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return false
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	return true
}

func (h *Handler) githubActionsPageData(r *http.Request, id int64) (githubActionsPageData, error) {
	item, err := h.applicationDetails.Get(r.Context(), id)
	if err != nil {
		return githubActionsPageData{}, err
	}
	services, err := h.applicationDetails.ListServices(r.Context(), id)
	if err != nil {
		return githubActionsPageData{}, err
	}
	data := githubActionsPageData{
		Application:  item,
		Services:     services,
		Branch:       "master",
		Dockerfile:   "Dockerfile",
		BuildContext: ".",
		ServerHost:   h.server.IPAddress,
	}
	if data.ServerHost == "Unavailable" {
		data.ServerHost = ""
	}
	for _, service := range services {
		if service.Type == application.ServiceTypeApplication || service.Type == "" {
			data.ServiceName = service.Name
			break
		}
	}
	if data.ServiceName == "" && len(services) > 0 {
		data.ServiceName = services[0].Name
	}
	if data.ServiceName != "" {
		data.ImageName = strings.ToLower(item.FolderName + "/" + data.ServiceName)
	}
	if integration, err := h.githubActions.Get(r.Context(), id); err == nil {
		data.Integration = &integration
		data.Repository = integration.Repository
		data.Branch = integration.Branch
		data.Dockerfile = integration.Dockerfile
		data.BuildContext = integration.BuildContext
		data.ServiceName = integration.ServiceName
		data.ImageName = integration.ImageName
		data.ServerHost = integration.ServerHost
	} else if !errors.Is(err, application.ErrGitHubActionsNotConfigured) {
		return githubActionsPageData{}, err
	}
	return data, nil
}

func (h *Handler) validRequestCSRF(r *http.Request) bool {
	expected := h.csrfToken
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		expected = cookie.Value
	}
	return validCSRFToken(r.Form.Get("csrf_token"), expected)
}

func (h *Handler) writeGitHubActionsPage(w http.ResponseWriter, r *http.Request, status int, data githubActionsPageData) {
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
	page.GitHubActionsPage = &data
	page.GitHubActionsProgress = data.Progress
	h.writeTemplateStatus(w, "github-actions.html", page, status)
}

func parseApplicationID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func githubActionsUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrGitHubActionsRepositoryRequired):
		return "Enter a GitHub repository in owner/repository form."
	case errors.Is(err, application.ErrGitHubActionsRepositoryInvalid):
		return "GitHub repositories must use a valid owner/repository name."
	case errors.Is(err, application.ErrGitHubActionsBranchRequired), errors.Is(err, application.ErrGitHubActionsBranchInvalid):
		return "Enter a valid Git branch name."
	case errors.Is(err, application.ErrGitHubActionsPathInvalid):
		return "Dockerfile and build context must be repository-relative paths."
	case errors.Is(err, application.ErrGitHubActionsHostRequired), errors.Is(err, application.ErrGitHubActionsHostInvalid):
		return "Enter the public IP address or hostname of this Redlaunch server."
	case errors.Is(err, application.ErrGitHubActionsImageRequired), errors.Is(err, application.ErrGitHubActionsImageInvalid):
		return "Enter a valid registry image repository without a tag."
	case errors.Is(err, application.ErrGitHubActionsServiceRequired), errors.Is(err, application.ErrServiceNotFound):
		return "Choose an existing application service."
	case errors.Is(err, application.ErrGitHubActionsNotConfigured):
		return "GitHub Actions is not configured for this application."
	default:
		return "The GitHub Actions deployment could not be configured."
	}
}
