package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

// apiPathPrefix namespaces machine endpoints that authenticate with Bearer
// API tokens instead of the session cookie. Browser middleware (session login
// and CSRF cookies) does not apply below this prefix; the Authorization
// header is not ambient, so cross-site requests cannot present it.
const apiPathPrefix = "/api/"

func isAPITokenPath(r *http.Request) bool {
	return r != nil && r.URL != nil && strings.HasPrefix(r.URL.Path, apiPathPrefix)
}

// apiTokenService is the operator-managed API token capability used by the
// machine endpoints (Authenticate) and the Settings tab
// (List/Create/Revoke).
type apiTokenService interface {
	Authenticate(context.Context, string) (application.APIToken, error)
	List(context.Context) ([]application.APIToken, error)
	Create(context.Context, application.APITokenInput) (application.APITokenSetup, error)
	Revoke(context.Context, int64) error
}

// bearerToken extracts a Bearer credential. Anything else (missing header,
// wrong scheme, extra material) is rejected without distinguishing cases.
func bearerToken(r *http.Request) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeAPIJSON(w, status, map[string]string{"error": message})
}

// authenticateAPIRequest verifies the Bearer token for a machine request. It
// writes the JSON error response itself and reports whether authentication
// succeeded. Token values are never logged.
func (h *Handler) authenticateAPIRequest(w http.ResponseWriter, r *http.Request) (application.APIToken, bool) {
	if h.apiTokens == nil {
		h.logger.Error("API request without a token service")
		writeAPIError(w, http.StatusInternalServerError, "API tokens are not configured.")
		return application.APIToken{}, false
	}
	plaintext, ok := bearerToken(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "Valid Bearer authentication is required.")
		return application.APIToken{}, false
	}
	token, err := h.apiTokens.Authenticate(r.Context(), plaintext)
	if err != nil {
		switch {
		case errors.Is(err, application.ErrAPITokenInvalid):
			writeAPIError(w, http.StatusUnauthorized, "Valid Bearer authentication is required.")
		case errors.Is(err, application.ErrAPITokenExpired):
			writeAPIError(w, http.StatusUnauthorized, "This API token has expired. Create a new token in Settings.")
		default:
			h.logger.Error("authenticate API token", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "The API token could not be checked.")
		}
		return application.APIToken{}, false
	}
	return token, true
}

// authorizeAPITokenForApplication enforces the per-application token scope:
// a token may only address its pinned application.
func authorizeAPITokenForApplication(w http.ResponseWriter, token application.APIToken, applicationID int64) bool {
	if token.Scope != application.APITokenScopeRun || token.ApplicationID != applicationID {
		writeAPIError(w, http.StatusForbidden, "This API token is not authorized for this application.")
		return false
	}
	return true
}

// runServiceViaAPI starts one registered service as a one-off task and
// returns a pollable job. It mirrors the Settings run-once action, but for
// machine callers: Bearer authentication, JSON responses, and asynchronous
// execution so long migrations do not hold the dispatch connection.
func (h *Handler) runServiceViaAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, http.StatusNotFound, "Application not found.")
		return
	}
	serviceName, err := application.ValidateServiceName(r.PathValue("service"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "The service name is invalid.")
		return
	}
	token, ok := h.authenticateAPIRequest(w, r)
	if !ok {
		return
	}
	if !authorizeAPITokenForApplication(w, token, id) {
		return
	}
	if !h.apiServiceRegistered(w, r.Context(), id, serviceName) {
		return
	}

	job, created, err := h.apiRunJobs.createUnique(id, serviceName)
	if err != nil {
		h.logger.Error("create API run job", "application_id", id, "service", serviceName, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "The run job could not be created.")
		return
	}
	if created {
		if err := h.startTrackedJob("api-run:"+strconv.FormatInt(id, 10)+":"+serviceName, func(ctx context.Context) {
			h.runAPIRunJob(ctx, job, token)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit API run job", "application_id", id, "service", serviceName, "error", err)
			writeAPIError(w, http.StatusServiceUnavailable, "The operation system is busy. Try again shortly.")
			return
		}
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]string{
		"status":     apiRunJobStateRunning,
		"job_id":     job.id,
		"status_url": apiRunStatusURL(id, serviceName, job.id),
	})
}

// apiRunJobStatus reports one machine-triggered run job. Unknown or expired
// job IDs report 404 like the backup status endpoint.
func (h *Handler) apiRunJobStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, http.StatusNotFound, "Application not found.")
		return
	}
	serviceName, err := application.ValidateServiceName(r.PathValue("service"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "The service name is invalid.")
		return
	}
	token, ok := h.authenticateAPIRequest(w, r)
	if !ok {
		return
	}
	if !authorizeAPITokenForApplication(w, token, id) {
		return
	}
	job := h.apiRunJobs.get(id, serviceName, r.URL.Query().Get("id"))
	if job == nil {
		writeAPIError(w, http.StatusNotFound, "Run job not found.")
		return
	}
	state, detail := job.snapshot()
	response := map[string]string{"status": state}
	if state == apiRunJobStateFailed && detail != "" {
		response["detail"] = detail
	}
	writeAPIJSON(w, http.StatusOK, response)
}

// apiServiceRegistered pre-checks that the service exists so machine callers
// get a fast 404 instead of a failed job. The run itself revalidates under
// the project lock.
func (h *Handler) apiServiceRegistered(w http.ResponseWriter, ctx context.Context, id int64, serviceName string) bool {
	services, err := h.applicationDetails.ListServices(ctx, id)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "Application not found.")
			return false
		}
		h.logger.Error("list services for API run", "application_id", id, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "The services could not be read.")
		return false
	}
	for _, service := range services {
		if service.Name == serviceName {
			return true
		}
	}
	writeAPIError(w, http.StatusNotFound, "Service not found.")
	return false
}

func (h *Handler) runAPIRunJob(ctx context.Context, job *apiRunJob, token application.APIToken) {
	if err := h.serviceActions.RunServiceOnce(ctx, job.applicationID, job.serviceName); err != nil {
		job.fail(err)
		h.logger.Error("run API service action",
			"application_id", job.applicationID,
			"service", job.serviceName,
			"token_id", token.ID,
			"token_prefix", token.Prefix,
			"error", serviceActionErrorDetail(err))
		return
	}
	h.logger.Info("ran API service action",
		"application_id", job.applicationID,
		"service", job.serviceName,
		"token_id", token.ID,
		"token_prefix", token.Prefix)
	job.complete()
}

func apiRunStatusURL(applicationID int64, serviceName, jobID string) string {
	return "/api/v1/applications/" + strconv.FormatInt(applicationID, 10) +
		"/services/" + url.PathEscape(serviceName) +
		"/run/status?id=" + url.QueryEscape(jobID)
}
