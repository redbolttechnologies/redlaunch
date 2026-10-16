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

// versionDeploymentService lists and switches the registry versions a managed
// service deploys.
type versionDeploymentService interface {
	ListServiceVersions(context.Context, int64, string) (application.ServiceVersions, error)
	DeployServiceVersion(context.Context, int64, string, string, func(string, string)) error
	ListApplicationVersions(context.Context, int64) (application.ApplicationVersions, error)
	DeployApplicationVersion(context.Context, int64, string, func(string, string, string)) ([]application.VersionDeployOutcome, error)
}

// deployServiceVersion moves one service's deployment tag to the selected
// image digest and recreates the service. The work runs as a tracked service
// action job so the service page keeps its progress toast.
func (h *Handler) deployServiceVersion(w http.ResponseWriter, r *http.Request) {
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
	if h.versionDeploys == nil {
		http.Error(w, "Version deployment is not configured.", http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.Error(w, "The service name is invalid.", http.StatusBadRequest)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The version deployment request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This version deployment page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	digest, err := application.ValidateRegistryDigest(r.Form.Get("digest"))
	if err != nil {
		http.Error(w, "The selected version is invalid.", http.StatusBadRequest)
		return
	}

	job, created, err := h.serviceActionJobs.createUnique(id, serviceName, "deploy")
	if err != nil {
		if errors.Is(err, ErrServiceActionBusy) {
			http.Error(w, "Another operation is already running for this service. Try again shortly.", http.StatusConflict)
			return
		}
		h.logger.Error("create version deployment job", "application_id", id, "service", serviceName, "error", err)
		http.Error(w, "The version deployment could not be started.", http.StatusInternalServerError)
		return
	}
	if created {
		job.setDeployTarget(digest)
		if err := h.startTrackedJob("service-action:"+strconv.FormatInt(id, 10)+":"+serviceName, func(ctx context.Context) {
			h.runServiceActionJob(ctx, job)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit version deployment job", "application_id", id, "service", serviceName, "error", err)
			http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	} else if job.takeDeployTarget() != digest {
		// A running deployment of a different version must not be silently
		// re-targeted; the operator chooses explicitly.
		http.Error(w, "Another version deployment is already running for this service. Try again shortly.", http.StatusConflict)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(id, serviceName)+"?service_action_job="+url.QueryEscape(job.id), http.StatusSeeOther)
}

// deployApplicationVersion moves every eligible service's deployment tag to
// the image tagged sourceTag in its own repository and recreates those
// services. The work runs as a tracked job with a per-service progress
// dialog.
func (h *Handler) deployApplicationVersion(w http.ResponseWriter, r *http.Request) {
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
	deployer, ok := h.versionDeploys.(applicationVersionDeployService)
	if !ok {
		http.Error(w, "Version deployment is not configured.", http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The version deployment request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This version deployment page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	sourceTag := strings.TrimSpace(r.Form.Get("tag"))
	if sourceTag == "" {
		http.Error(w, "Choose a version tag to deploy.", http.StatusBadRequest)
		return
	}
	if _, err := application.ValidateRegistryTag(sourceTag); err != nil {
		http.Error(w, "The version tag is invalid.", http.StatusBadRequest)
		return
	}

	job, created, err := h.applicationVersionJobs.createUnique(id, sourceTag)
	if err != nil {
		if errors.Is(err, ErrApplicationVersionBusy) {
			http.Error(w, "Another version deployment is already running for this application. Try again shortly.", http.StatusConflict)
			return
		}
		h.logger.Error("create application version deployment job", "application_id", id, "error", err)
		http.Error(w, "The version deployment could not be started.", http.StatusInternalServerError)
		return
	}
	if created {
		if err := h.startTrackedJob("application-version-deploy:"+strconv.FormatInt(id, 10), func(ctx context.Context) {
			h.runApplicationVersionJob(ctx, job, deployer)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit application version deployment job", "application_id", id, "error", err)
			http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}
	http.Redirect(w, r, "/applications/"+strconv.FormatInt(id, 10)+"?version_deploy_job="+url.QueryEscape(job.id), http.StatusSeeOther)
}

// applicationVersionStatus returns the progress dialog fragment polled while
// an application-wide version deployment runs.
func (h *Handler) applicationVersionStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The version deployment job ID is required.", http.StatusBadRequest)
		return
	}
	progress, ok := h.applicationVersionProgress(id, jobID)
	if !ok {
		http.Error(w, "The version deployment job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "application-version-progress.html", pageData{ApplicationVersionProgress: progress}, http.StatusOK)
}

func (h *Handler) applicationVersionProgress(applicationID int64, jobID string) (*applicationVersionProgressData, bool) {
	if jobID == "" {
		return nil, true
	}
	if h.applicationVersionJobs == nil {
		return nil, false
	}
	job := h.applicationVersionJobs.get(applicationID, jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/applications/" + strconv.FormatInt(applicationID, 10) + "/versions/deploy/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/applications/" + strconv.FormatInt(applicationID, 10)
	return &progress, true
}

// serviceVersions loads the version listing for the service details page.
// Services without local-registry images and registry failures render the
// page without the Versions widget instead of failing it.
func (h *Handler) serviceVersions(ctx context.Context, applicationID int64, serviceName string) application.ServiceVersions {
	if h.versionDeploys == nil {
		return application.ServiceVersions{}
	}
	versions, err := h.versionDeploys.ListServiceVersions(ctx, applicationID, serviceName)
	if err != nil {
		if !errors.Is(err, application.ErrServiceVersionUnavailable) && !errors.Is(err, application.ErrServiceNotFound) {
			h.logger.Error("list service versions", "application_id", applicationID, "service", serviceName, "error", err)
		}
		return application.ServiceVersions{}
	}
	return versions
}

// applicationVersions loads the tag availability behind the application-wide
// version deployment dialog.
func (h *Handler) applicationVersions(ctx context.Context, applicationID int64) application.ApplicationVersions {
	if h.versionDeploys == nil {
		return application.ApplicationVersions{}
	}
	versions, err := h.versionDeploys.ListApplicationVersions(ctx, applicationID)
	if err != nil {
		if !errors.Is(err, application.ErrNotFound) {
			h.logger.Error("list application versions", "application_id", applicationID, "error", err)
		}
		return application.ApplicationVersions{}
	}
	return versions
}

// versionDigestShort abbreviates a manifest digest for dense listings; the
// full digest stays available as the element title.
func versionDigestShort(digest string) string {
	digest = strings.TrimSpace(digest)
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || len(encoded) < 12 {
		return digest
	}
	return algorithm + ":" + encoded[:12] + "…"
}
