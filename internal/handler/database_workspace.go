package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"redlaunch/internal/application"
)

type databaseWorkspaceService interface {
	GetDatabaseWorkspace(context.Context, string) (application.ManagedDatabase, []application.DatabaseSchema, error)
	RunDatabaseQuery(context.Context, string, string) (application.DatabaseQueryResult, error)
}

type databaseWorkspacePageData struct {
	Database    application.ManagedDatabase
	Schemas     []application.DatabaseSchema
	SQL         string
	Result      *application.DatabaseQueryResult
	Error       string
	Available   bool
	ImportOpen  bool
	ImportError string
	Notice      string
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
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
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
	data.Available = err == nil
	data.ImportOpen = r.URL.Query().Get("import") == "1"
	if r.URL.Query().Get("notice") == "dump-imported" {
		data.Notice = "Database dump imported successfully."
	}
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
	h.writeDatabaseWorkspacePage(w, r, http.StatusOK, data)
}

func (h *Handler) writeDatabaseWorkspacePage(w http.ResponseWriter, r *http.Request, status int, data databaseWorkspacePageData) {
	page := h.shellPageData(r)
	page.ActivePage = "databases"
	page.CSRFToken = h.setCSRFCookie(w, r)
	page.DatabaseWorkspacePage = &data
	h.writeTemplateStatus(w, "database-workspace.html", page, status)
}

func (h *Handler) importDatabaseDump(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
	_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, application.DatabaseDumpMaxBytes+1024*1024)
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		http.Error(w, "Choose a dump file up to 32 MiB.", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if !h.validRequestCSRF(r) {
		http.Error(w, "Invalid CSRF token.", http.StatusForbidden)
		return
	}
	workspace, ok := h.managedDatabases.(databaseWorkspaceService)
	importer, importOK := h.managedDatabases.(interface {
		ImportDatabaseDump(context.Context, string, io.Reader) error
	})
	if !ok || !importOK {
		http.Error(w, "Database dump import is not configured.", http.StatusServiceUnavailable)
		return
	}
	item, schemas, err := workspace.GetDatabaseWorkspace(r.Context(), r.PathValue("name"))
	if errors.Is(err, application.ErrManagedDatabaseNotFound) {
		http.NotFound(w, r)
		return
	}
	data := databaseWorkspacePageData{Database: item, Schemas: schemas, Available: err == nil, ImportOpen: true}
	if err != nil {
		data.ImportError = "The database is unavailable or its owner does not have a safe restore role."
	} else if r.PostForm.Get("confirm") != "1" {
		data.ImportError = "Confirm that this import may change existing database objects and data."
	} else {
		file, header, fileErr := r.FormFile("dump")
		if fileErr != nil {
			data.ImportError = "Choose a SQL or custom-format dump file."
		} else {
			defer file.Close()
			if header.Size == 0 || header.Size > application.DatabaseDumpMaxBytes {
				data.ImportError = "Choose a non-empty dump file up to 32 MiB."
			} else if err := importer.ImportDatabaseDump(r.Context(), item.Name, file); err != nil {
				data.ImportError = "Import failed. Check the format, PostgreSQL version, owner permissions, and conflicting objects. SQL dumps must not contain psql commands other than pg_dump's restriction envelope."
			} else {
				http.Redirect(w, r, managedDatabaseWorkspacePath(item.Name)+"?notice=dump-imported", http.StatusSeeOther)
				return
			}
		}
	}
	h.writeDatabaseWorkspacePage(w, r, http.StatusUnprocessableEntity, data)
}
