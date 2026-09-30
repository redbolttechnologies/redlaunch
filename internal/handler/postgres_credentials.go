package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

type postgresCredentialsUpdater interface {
	UpdatePostgreSQLCredentials(context.Context, int64, string, application.PostgreSQLCredentialsInput) (application.Service, error)
}

func (h *Handler) postgresCredentialsService() postgresCredentialsUpdater {
	if updater, ok := h.serviceActions.(postgresCredentialsUpdater); ok && updater != nil {
		return updater
	}
	if updater, ok := h.postgresManager.(postgresCredentialsUpdater); ok && updater != nil {
		return updater
	}
	if updater, ok := h.serviceDetails.(postgresCredentialsUpdater); ok && updater != nil {
		return updater
	}
	return nil
}

func (h *Handler) runPostgresCredentialsJob(ctx context.Context, job *serviceActionJob) error {
	user, password, ok := job.takeCredentials()
	defer job.wipeCredentials()
	if !ok {
		return errors.New("database credentials were not provided")
	}
	updater := h.postgresCredentialsService()
	if updater == nil {
		return errors.New("database credential updates are not configured")
	}
	_, err := updater.UpdatePostgreSQLCredentials(ctx, job.applicationID, job.serviceName, application.PostgreSQLCredentialsInput{
		DatabaseUser:     user,
		DatabasePassword: password,
	})
	return err
}

// updatePostgresCredentials validates the credential form and queues the
// rotation as a service-action job so it serializes with start/stop/restart
// and reports progress through the existing service toast.
func (h *Handler) updatePostgresCredentials(w http.ResponseWriter, r *http.Request) {
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
	serviceName := r.PathValue("service")
	validatedServiceName, err := application.ValidateServiceName(serviceName)
	if err != nil || validatedServiceName != serviceName {
		http.Error(w, "The service name is invalid.", http.StatusBadRequest)
		return
	}

	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The credential update request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This service page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}

	user := strings.TrimSpace(r.Form.Get("database_user"))
	newPassword := r.Form.Get("new_password")
	confirmPassword := r.Form.Get("confirm_password")
	if newPassword != confirmPassword {
		http.Error(w, "The new passwords do not match.", http.StatusBadRequest)
		return
	}
	if _, err := application.ValidateDatabaseUser(user); err != nil {
		http.Error(w, postgresCredentialsUserMessage(err), http.StatusBadRequest)
		return
	}
	if _, err := application.ValidateDatabasePassword(newPassword); err != nil {
		http.Error(w, "The new password is invalid.", http.StatusBadRequest)
		return
	}

	if h.postgresCredentialsService() == nil {
		h.logger.Error("update database credentials without an updater", "application_id", id, "service", serviceName)
		http.Error(w, "Database credential updates are not configured.", http.StatusInternalServerError)
		return
	}

	job, created, err := h.serviceActionJobs.createUnique(id, serviceName, "credentials")
	if err != nil {
		if errors.Is(err, ErrServiceActionBusy) {
			http.Error(w, "Another operation is already running for this service. Try again shortly.", http.StatusConflict)
			return
		}
		h.logger.Error("create database credentials job", "application_id", id, "service", serviceName, "error", err)
		http.Error(w, "The credential update could not be started.", http.StatusInternalServerError)
		return
	}
	if created {
		job.setCredentials(user, newPassword)
		if err := h.startTrackedJob("service-action:"+strconv.FormatInt(id, 10)+":"+serviceName, func(ctx context.Context) {
			h.runServiceActionJob(ctx, job)
		}); err != nil {
			job.wipeCredentials()
			job.fail(err)
			h.logger.Error("admit database credentials job", "application_id", id, "service", serviceName, "error", err)
			http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}
	location := serviceDetailsPath(id, serviceName) + "?service_action_job=" + job.id
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func postgresCredentialsUserMessage(err error) string {
	switch {
	case errors.Is(err, application.ErrDatabaseUserRequired):
		return "The database user is required."
	case errors.Is(err, application.ErrDatabaseUserTooLong):
		return "The database user is too long."
	default:
		return "The database user is invalid. Use letters, numbers, and underscores, starting with a letter or underscore."
	}
}
