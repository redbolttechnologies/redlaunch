package handler

import (
	"context"
	"net/http"
	"net/url"
)

func (h *Handler) updateRedlaunch(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "The update request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	if h.selfUpdater == nil {
		h.logger.Error("update Redlaunch without an updater")
		http.Error(w, "The update service is not configured.", http.StatusInternalServerError)
		return
	}

	job, created, err := h.selfUpdateJobs.createUnique()
	if err != nil {
		h.logger.Error("create self-update job", "error", err)
		http.Error(w, "The update job could not be created.", http.StatusInternalServerError)
		return
	}
	if created {
		if err := h.startTrackedJob("self-update", func(ctx context.Context) {
			h.runSelfUpdateJob(ctx, job)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit self-update job", "error", err)
			http.Error(w, "The update system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}

	location := "/settings?update_job=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) selfUpdateStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The update job ID is required.", http.StatusBadRequest)
		return
	}
	job := h.selfUpdateJobs.get(jobID)
	if job == nil {
		http.Error(w, "The update job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	progress := job.snapshot()
	progress.StatusURL = "/settings/update/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/settings"
	h.writeTemplateStatus(w, "selfupdate-progress.html", pageData{SelfUpdateProgress: &progress}, http.StatusOK)
}

func (h *Handler) selfUpdateProgress(r *http.Request) (*selfUpdateProgressData, bool) {
	jobID := r.URL.Query().Get("update_job")
	if jobID == "" {
		return nil, true
	}
	job := h.selfUpdateJobs.get(jobID)
	if job == nil {
		return nil, false
	}
	progress := job.snapshot()
	progress.StatusURL = "/settings/update/status?id=" + url.QueryEscape(jobID)
	progress.CloseURL = "/settings"
	return &progress, true
}

func (h *Handler) runSelfUpdateJob(ctx context.Context, job *selfUpdateJob) {
	if h.selfUpdater == nil {
		job.fail(context.Canceled)
		h.logger.Error("run self-update without an updater")
		return
	}
	// The service pulls synchronously and then hands the container rebuild
	// to a detached helper, so this job finishes before the manager
	// restarts instead of being killed mid-recreate.
	if err := h.selfUpdater.QueueUpdateWithProgress(ctx, job.update); err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("update Redlaunch", "stage", snapshot.ErrorStage, "error", err)
		return
	}
	job.complete()
}
