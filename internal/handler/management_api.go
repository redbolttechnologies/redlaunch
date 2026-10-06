package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"redlaunch/internal/application"
)

// managementJob retains only a sanitized result, never request arguments or
// environment values. All management tokens can poll server-wide jobs.
type managementJob struct {
	mu         sync.RWMutex
	status     string
	result     any
	finishedAt time.Time
}

type managementJobStore struct {
	mu   sync.Mutex
	jobs map[string]*managementJob
}

func (s *managementJobStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.RLock()
		finished := j.finishedAt
		j.mu.RUnlock()
		if !finished.IsZero() && now.Sub(finished) > time.Hour {
			delete(s.jobs, id)
		}
	}
}

func (h *Handler) authenticateManagementRequest(w http.ResponseWriter, r *http.Request) bool {
	token, ok := h.authenticateAPIRequest(w, r)
	if !ok {
		return false
	}
	if token.Scope != application.APITokenScopeManage || token.ApplicationID != 0 {
		writeAPIError(w, http.StatusForbidden, "A server-wide management token is required.")
		return false
	}
	return true
}

// managementAPI exposes a fixed typed operation catalog, not arbitrary Docker,
// SQL, shell, file, or service-method invocation.
func (h *Handler) managementAPI(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateManagementRequest(w, r) {
		return
	}
	op, ok := application.ManagementOperationByName(r.PathValue("operation"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "Management operation not found.")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid management request.")
		return
	}
	args, err := application.DecodeManagementArguments(op, raw)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "Invalid management arguments.")
		return
	}
	if op.ReadOnly {
		result, err := h.executeManagement(r.Context(), op.Name, args)
		if err != nil {
			writeManagementError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"status": "complete", "result": result})
		return
	}
	id, err := newCSRFToken()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "Could not create management job.")
		return
	}
	// Reserve worker capacity before recording the job or launching side effects.
	lease, err := h.jobs.acquire("management:" + id)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "The operation system is busy.")
		return
	}
	job := &managementJob{status: "running"}
	h.managementJobs.expire(time.Now())
	h.managementJobs.mu.Lock()
	if len(h.managementJobs.jobs) >= 1024 {
		h.managementJobs.mu.Unlock()
		lease.cancel()
		writeAPIError(w, http.StatusServiceUnavailable, "Management job retention capacity is full.")
		return
	}
	h.managementJobs.jobs[id] = job
	h.managementJobs.mu.Unlock()
	lease.start(func(ctx context.Context) {
		result, err := h.executeManagement(ctx, op.Name, args)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.status = "complete"
		job.result = result
		if err != nil {
			job.status = "failed"
			job.result = nil
			h.logger.Warn("management operation failed", "operation", op.Name, "job_id", id)
		}
		job.finishedAt = time.Now()
	})
	writeAPIJSON(w, http.StatusAccepted, map[string]string{"status": "running", "job_id": id})
}

func (h *Handler) managementJobStatus(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateManagementRequest(w, r) {
		return
	}
	h.managementJobs.expire(time.Now())
	h.managementJobs.mu.Lock()
	job := h.managementJobs.jobs[r.PathValue("job")]
	h.managementJobs.mu.Unlock()
	if job == nil {
		writeAPIError(w, http.StatusNotFound, "Management job not found.")
		return
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	response := map[string]any{"status": job.status}
	if job.status == "complete" {
		response["result"] = job.result
	}
	if job.status == "failed" {
		response["error"] = "Management operation failed. Inspect the resource before retrying."
	}
	writeAPIJSON(w, http.StatusOK, response)
}

func writeManagementError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrManagedDatabaseNotFound) || errors.Is(err, application.ErrManagedDatabaseUserNotFound) {
		status = http.StatusNotFound
	}
	// Do not forward service errors: even wrapped validation or Docker errors
	// can contain environment values, credentials, or arbitrary process output.
	writeAPIError(w, status, "Management operation could not be completed.")
}

var errManagementUnavailable = errors.New("management service is unavailable")

func (h *Handler) executeManagement(ctx context.Context, op string, a application.ManagementArguments) (any, error) {
	success := map[string]bool{"updated": true}
	switch op {
	case "list_applications":
		if h.applicationManager != nil {
			return h.applicationManager.List(ctx)
		}
	case "get_application":
		if h.applicationDetails != nil {
			return h.applicationDetails.Get(ctx, a.ApplicationID)
		}
	case "create_application":
		if h.applicationManager != nil {
			return h.applicationManager.Create(ctx, a.Name, a.FolderName)
		}
	case "rename_application":
		if h.applicationRenamer != nil {
			return h.applicationRenamer.RenameApplication(ctx, a.ApplicationID, a.Name)
		}
	case "delete_application":
		if h.applicationDeletion != nil {
			return success, h.applicationDeletion.DeleteApplication(ctx, a.ApplicationID)
		}
	case "list_services":
		if h.applicationDetails != nil {
			return h.applicationDetails.ListServices(ctx, a.ApplicationID)
		}
	case "create_service":
		if h.applicationContainerManager != nil {
			return h.applicationContainerManager.CreateApplicationService(ctx, a.ApplicationID, a.Configuration)
		}
	case "update_service":
		if updater := h.applicationContainerUpdateService(); updater != nil {
			return updater.UpdateApplicationService(ctx, a.ApplicationID, a.ServiceName, a.Configuration)
		}
	case "delete_service":
		if h.serviceDeletion != nil {
			return success, h.serviceDeletion.DeleteService(ctx, a.ApplicationID, a.ServiceName)
		}
	case "service_action":
		if h.serviceActions != nil {
			switch a.Action {
			case "start":
				return success, h.serviceActions.StartService(ctx, a.ApplicationID, a.ServiceName)
			case "stop":
				return success, h.serviceActions.StopService(ctx, a.ApplicationID, a.ServiceName)
			case "restart":
				return success, h.serviceActions.RestartService(ctx, a.ApplicationID, a.ServiceName)
			case "run":
				return success, h.serviceActions.RunServiceOnce(ctx, a.ApplicationID, a.ServiceName)
			}
		}
	case "list_environment":
		if h.applicationEnvironment != nil {
			files, err := h.applicationEnvironment.GetEnvironmentFiles(ctx, a.ApplicationID)
			if err != nil {
				return nil, err
			}
			// Even vars.env may contain misplaced credentials. Return names only.
			variables, secrets := []string{}, []string{}
			for _, v := range files.Variables {
				variables = append(variables, v.Key)
			}
			for _, v := range files.Secrets {
				secrets = append(secrets, v.Key)
			}
			return map[string]any{"variables": variables, "secrets": secrets, "variables_available": files.VariablesAvailable, "secrets_available": files.SecretsAvailable}, nil
		}
	case "add_variable":
		if h.applicationEditor != nil {
			return success, h.applicationEditor.AddEnvironmentVariable(ctx, a.ApplicationID, a.Name, a.Value)
		}
	case "update_variable":
		if h.applicationEditor != nil {
			return success, h.applicationEditor.UpdateEnvironmentVariable(ctx, a.ApplicationID, a.OriginalName, a.Name, a.Value)
		}
	case "delete_variable":
		if h.applicationEditor != nil {
			return success, h.applicationEditor.DeleteEnvironmentVariable(ctx, a.ApplicationID, a.Name)
		}
	case "add_secret":
		if h.applicationEditor != nil {
			return success, h.applicationEditor.AddEnvironmentSecret(ctx, a.ApplicationID, a.Name, a.Value)
		}
	case "update_secret":
		if editor, ok := h.applicationEditor.(applicationEnvironmentSecretValueEditor); ok {
			return success, editor.UpdateEnvironmentSecretValue(ctx, a.ApplicationID, a.OriginalName, a.Name, a.Value, true)
		}
		if h.applicationEditor != nil {
			return success, h.applicationEditor.UpdateEnvironmentSecret(ctx, a.ApplicationID, a.OriginalName, a.Name, a.Value)
		}
	case "delete_secret":
		if h.applicationEditor != nil {
			return success, h.applicationEditor.DeleteEnvironmentSecret(ctx, a.ApplicationID, a.Name)
		}
	case "list_domains":
		if h.applicationDomains != nil {
			return h.applicationDomains.ListDomains(ctx, a.ApplicationID)
		}
	case "create_domain":
		if h.applicationDomains != nil {
			return h.applicationDomains.CreateDomain(ctx, a.ApplicationID, a.Name)
		}
	case "delete_domain":
		if h.applicationDomains != nil {
			return success, h.applicationDomains.DeleteDomain(ctx, a.ApplicationID, a.Name)
		}
	case "list_routings":
		if h.applicationRoutings != nil {
			return h.applicationRoutings.ListRoutings(ctx, a.ApplicationID, a.DomainID)
		}
	case "create_routing":
		if h.applicationRoutings != nil {
			return h.applicationRoutings.CreateRouting(ctx, a.ApplicationID, a.DomainID, a.Routing)
		}
	case "update_routing":
		if h.applicationRoutings != nil {
			return success, h.applicationRoutings.UpdateRouting(ctx, a.ApplicationID, a.DomainID, a.RoutingID, a.Routing)
		}
	case "delete_routing":
		if h.applicationRoutings != nil {
			return success, h.applicationRoutings.DeleteRouting(ctx, a.ApplicationID, a.DomainID, a.RoutingID)
		}
	}
	if h.managedDatabases != nil {
		db := h.managedDatabases
		switch op {
		case "get_database_cluster":
			return db.GetCluster(ctx)
		case "enable_database_cluster":
			return success, db.EnableCluster(ctx, application.ManagedDatabaseEnableInput{Provider: a.Provider, Version: a.Version, DefaultUser: a.DefaultUser, Password: a.Password})
		case "disable_database_cluster":
			return success, db.DisableCluster(ctx)
		case "database_cluster_action":
			switch a.Action {
			case "start":
				return success, db.StartCluster(ctx)
			case "stop":
				return success, db.StopCluster(ctx)
			case "restart":
				return success, db.RestartCluster(ctx)
			}
		case "list_databases":
			return db.ListDatabases(ctx)
		case "create_database":
			result, err := db.CreateDatabase(ctx, application.ManagedDatabaseCreateInput{Name: a.Name, Owner: a.Owner, RequireExistingOwner: true})
			return result.ManagedDatabase, err
		case "delete_database":
			return success, db.DropDatabase(ctx, a.Name)
		case "list_database_users":
			return db.ListUsers(ctx)
		case "create_database_user":
			return success, db.CreateUser(ctx, application.ManagedDatabaseUserInput{Username: a.Username, Password: a.Password, Databases: a.Databases})
		case "update_database_user_password":
			return success, db.UpdateUserPassword(ctx, a.Username, a.Password)
		case "set_database_user_permissions":
			return success, db.SetUserPermissions(ctx, a.Username, a.Databases)
		case "delete_database_user":
			return success, db.DeleteUser(ctx, a.Username)
		}
	}
	return nil, errManagementUnavailable
}
