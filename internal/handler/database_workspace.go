package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"redlaunch/internal/application"
)

type databaseWorkspaceService interface {
	GetDatabaseWorkspace(context.Context, string) (application.ManagedDatabase, []application.DatabaseSchema, error)
	RunDatabaseQuery(context.Context, string, string) (application.DatabaseQueryResult, error)
}

type databaseWorkspacePageData struct {
	Database application.ManagedDatabase
	Schemas  []application.DatabaseSchema
	SQL      string
	Result   *application.DatabaseQueryResult
	Error    string
}

func managedDatabaseWorkspacePath(name string) string {
	segment := url.PathEscape(name)
	if name == "." {
		segment = "%2E"
	} else if name == ".." {
		segment = "%2E%2E"
	}
	return "/databases/" + segment
}

func (h *Handler) databaseWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "The SQL form is invalid or too large.", http.StatusBadRequest)
			return
		}
		if !h.validRequestCSRF(r) {
			http.Error(w, "Invalid CSRF token.", http.StatusForbidden)
			return
		}
	}
	workspace, ok := h.managedDatabases.(databaseWorkspaceService)
	if !ok {
		http.Error(w, "Database workspace is not configured.", http.StatusServiceUnavailable)
		return
	}
	data := databaseWorkspacePageData{}
	item, schemas, err := workspace.GetDatabaseWorkspace(r.Context(), r.PathValue("name"))
	if errors.Is(err, application.ErrManagedDatabaseNotFound) {
		http.NotFound(w, r)
		return
	}
	data.Database = item
	data.Schemas = schemas
	if err != nil {
		data.Error = "The database schemas could not be loaded. Check that the cluster is running and the owner is a non-administrative role without host-access privileges."
	}
	if r.Method == http.MethodPost {
		data.SQL = r.PostForm.Get("sql")
		if r.PostForm.Get("action") == "show-data" {
			data.SQL = ""
			for _, schema := range schemas {
				for _, table := range schema.Tables {
					if schema.Name == r.PostForm.Get("schema") && table.Name == r.PostForm.Get("table") {
						data.SQL = table.SQL
					}
				}
			}
		}
		if err == nil {
			result, queryErr := workspace.RunDatabaseQuery(r.Context(), item.Name, data.SQL)
			if queryErr != nil {
				data.Error = "SQL execution failed. Check the statement, permissions, and database availability. Queries have a 30-second timeout and a 4 MiB output limit."
			} else {
				data.Result = &result
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			h.writeTemplateStatus(w, "database-query-result.html", pageData{DatabaseWorkspacePage: &data}, http.StatusOK)
			return
		}
	}
	page := h.shellPageData(r)
	page.ActivePage = "databases"
	page.CSRFToken = h.setCSRFCookie(w, r)
	page.DatabaseWorkspacePage = &data
	h.writeTemplateStatus(w, "database-workspace.html", page, http.StatusOK)
}
