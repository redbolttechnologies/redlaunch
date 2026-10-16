package handler

import (
	"errors"
	"net/http"
	"strconv"

	"redlaunch/internal/application"
)

func (h *Handler) revokeMCPConnection(w http.ResponseWriter, r *http.Request) {
	if !h.oauthAuthorizationEnabled() {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The connection revoke request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Refresh it and try again.", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.Form.Get("id"), 10, 64)
	if err != nil || id < 1 || r.Form.Get("confirm") != "yes" {
		http.Error(w, "Select a connection and confirm revocation.", http.StatusBadRequest)
		return
	}
	if err := h.oauth.RevokeConnection(r.Context(), id); err != nil {
		if errors.Is(err, application.ErrOAuthGrantNotFound) {
			http.Error(w, "The connection could not be found. Refresh the page and try again.", http.StatusNotFound)
			return
		}
		h.logger.Error("revoke MCP connection", "error", err)
		data, loadErr := h.loadSettingsPageData(r.Context())
		if loadErr != nil {
			http.Error(w, "The settings could not be read.", http.StatusInternalServerError)
			return
		}
		for _, connection := range data.MCPConnections {
			if connection.ID == id {
				data.MCPDelete = &mcpConnectionDeletePageData{ID: id, ClientName: connection.ClientName,
					Resource: connection.Resource, Error: "The connection could not be revoked right now. Try again."}
			}
		}
		data.MCPActive = true
		h.writeSettingsPage(w, r, http.StatusInternalServerError, data)
		return
	}
	http.Redirect(w, r, "/settings?tab=mcp", http.StatusSeeOther)
}
