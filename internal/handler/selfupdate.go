package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"redlaunch/internal/application"
)

func (h *Handler) checkRedlaunchUpdate(w http.ResponseWriter, r *http.Request) {
	if h.selfUpdater == nil {
		h.writeTemplateStatus(w, "settings-update.html", pageData{
			SettingsPage: &settingsPageData{UpdateError: "The update service is not configured."},
		}, http.StatusOK)
		return
	}
	status, err := h.selfUpdater.CheckForUpdate(r.Context())
	data := settingsPageData{
		UpdateCurrentVersion: h.selfUpdater.CurrentVersion(),
		CSRFToken:            h.setCSRFCookie(w, r),
	}
	if err != nil {
		data.UpdateError = "The latest Redlaunch release could not be checked."
	} else {
		data.UpdateStatus = &status
	}
	if state, ok, stateErr := h.selfUpdater.UpdateState(r.Context()); stateErr == nil && ok {
		data.UpdateState = &state
	}
	h.writeTemplateStatus(w, "settings-update.html", pageData{SettingsPage: &data}, http.StatusOK)
}

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

	status, err := h.selfUpdater.CheckForUpdate(r.Context())
	if err != nil {
		h.logger.Error("check Redlaunch release before update", "error", err)
		http.Error(w, "The latest Redlaunch release could not be checked.", http.StatusBadGateway)
		return
	}
	if !status.UpdateAvailable || !status.Compatible || status.Release == nil {
		location := "/settings?update=current"
		if !status.Compatible {
			location = "/settings?update=incompatible"
		}
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}

	// A helper can fail before recreating the manager. Refresh the matching
	// job's durable outcome before admission instead of treating its in-memory
	// handoff marker as permanently active. If the helper is gone but the
	// operation is still non-terminal, resolve it now rather than requiring a
	// manager restart.
	state, stateOK, stateErr := h.selfUpdater.UpdateState(r.Context())
	if stateErr != nil {
		http.Error(w, "The update state could not be read. Try again shortly.", http.StatusServiceUnavailable)
		return
	}
	if stateOK {
		h.selfUpdateJobs.reconcile(state)
		needsReconcile := !state.Terminal() || h.selfUpdater.UpdateInProgress()
		existing := h.selfUpdateJobs.get(state.OperationID)
		if needsReconcile && (existing == nil || existing.snapshot().State != selfUpdateStateRunning) {
			reconcileCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			reconcileErr := h.selfUpdater.ReconcileInterruptedUpdate(reconcileCtx)
			cancel()
			if reconcileErr != nil && !errors.Is(reconcileErr, application.ErrUpdateUncertain) && !errors.Is(reconcileErr, application.ErrUpdateBusy) {
				h.logger.Error("reconcile interrupted update", "error", reconcileErr)
				http.Error(w, "The previous update could not be reconciled. Try again shortly.", http.StatusServiceUnavailable)
				return
			}
			if updated, ok, err := h.selfUpdater.UpdateState(r.Context()); err == nil && ok {
				h.selfUpdateJobs.reconcile(updated)
			}
		}
	}
	job, created, err := h.selfUpdateJobs.createUnique()
	if err != nil {
		h.logger.Error("create self-update job", "error", err)
		http.Error(w, "The update job could not be created.", http.StatusInternalServerError)
		return
	}
	if created {
		if err := h.startTrackedJob("self-update", func(ctx context.Context) {
			h.runSelfUpdateJob(ctx, job, status)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit self-update job", "error", err)
			http.Error(w, "The update system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}

	location := "/settings?update_job=" + url.QueryEscape(job.id) + "&operation=" + url.QueryEscape(job.id)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) selfUpdateStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "The update job ID is required.", http.StatusBadRequest)
		return
	}
	job := h.selfUpdateJobs.get(jobID)
	operationID := strings.TrimSpace(r.URL.Query().Get("operation"))
	expectedOperation := operationID
	if job != nil {
		// The tracked job ID is also the durable operation ID supplied to the
		// service, so persisted state cannot be mixed across operations.
		expectedOperation = jobID
	}
	var persisted *application.UpdateState
	if h.selfUpdater != nil && expectedOperation != "" {
		if state, ok, err := h.selfUpdater.UpdateState(r.Context()); err == nil && ok && state.OperationID == expectedOperation {
			persisted = &state
		}
	}
	if persisted != nil {
		h.selfUpdateJobs.reconcile(*persisted)
	}
	w.Header().Set("Cache-Control", "no-store")
	var progress selfUpdateProgressData
	switch {
	case job != nil && job.snapshot().State == selfUpdateStateHandoff && persisted != nil:
		// After handoff the durable operation, not the manager's in-memory
		// handoff marker, determines success, failure, and uncertainty.
		progress = persistedUpdateProgress(*persisted)
	case job != nil:
		progress = job.snapshot()
	case persisted != nil:
		progress = persistedUpdateProgress(*persisted)
	default:
		http.Error(w, "The update job was not found.", http.StatusNotFound)
		return
	}
	if persisted != nil {
		progress.UpdateState = persisted
	}
	progress.JobID = jobID
	progress.StatusURL = "/settings/update/status?id=" + url.QueryEscape(jobID) + "&operation=" + url.QueryEscape(expectedOperation)
	progress.CloseURL = "/settings"
	h.writeTemplateStatus(w, "selfupdate-progress.html", pageData{SelfUpdateProgress: &progress}, http.StatusOK)
}

func (h *Handler) selfUpdateProgress(r *http.Request) (*selfUpdateProgressData, bool) {
	jobID := r.URL.Query().Get("update_job")
	if jobID == "" {
		return nil, true
	}
	operationID := strings.TrimSpace(r.URL.Query().Get("operation"))
	job := h.selfUpdateJobs.get(jobID)
	var persisted *application.UpdateState
	if h.selfUpdater != nil {
		expected := operationID
		if job != nil {
			expected = jobID
		}
		if expected != "" {
			if state, ok, err := h.selfUpdater.UpdateState(r.Context()); err == nil && ok && state.OperationID == expected {
				persisted = &state
			}
		}
	}
	if persisted != nil {
		h.selfUpdateJobs.reconcile(*persisted)
	}
	var progress selfUpdateProgressData
	switch {
	case job == nil && persisted != nil:
		progress = persistedUpdateProgress(*persisted)
	case job == nil:
		return nil, false
	case job.snapshot().State == selfUpdateStateHandoff && persisted != nil:
		progress = persistedUpdateProgress(*persisted)
	default:
		progress = job.snapshot()
	}
	if persisted != nil {
		progress.UpdateState = persisted
	}
	statusOperation := operationID
	if job != nil {
		statusOperation = jobID
	}
	progress.JobID = jobID
	progress.StatusURL = "/settings/update/status?id=" + url.QueryEscape(jobID) + "&operation=" + url.QueryEscape(statusOperation)
	progress.CloseURL = "/settings"
	return &progress, true
}

func (h *Handler) runSelfUpdateJob(ctx context.Context, job *selfUpdateJob, status application.UpdateStatus) {
	if h.selfUpdater == nil {
		job.fail(context.Canceled)
		h.logger.Error("run self-update without an updater")
		return
	}
	if err := h.selfUpdater.QueueReleaseUpdateWithOperationID(ctx, job.id, status, job.update); err != nil {
		job.fail(err)
		snapshot := job.snapshot()
		h.logger.Error("update Redlaunch", "stage", snapshot.ErrorStage, "error", err)
		return
	}
	job.completeHandoff()
}
