package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"redlaunch/internal/application"
	"redlaunch/internal/mcp"
)

const protectedResourcePrefix = "/.well-known/oauth-protected-resource"

func isMCPMachinePath(path string) bool {
	path = strings.TrimPrefix(path, protectedResourcePrefix)
	const origin = "https://redlaunch.internal"
	_, err := application.ParseOAuthResource(origin, origin+path)
	return err == nil
}

func (h *Handler) registerMachineAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/management/{operation}", h.managementAPI)
	mux.HandleFunc("GET /api/v1/management/jobs/{job}", h.managementJobStatus)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/run", h.runServiceViaAPI)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/run/status", h.apiRunJobStatus)
}

func (h *Handler) mcpResource(r *http.Request, path string) (application.OAuthResource, string, error) {
	origin, err := h.oauthOrigin(r)
	if err != nil {
		return application.OAuthResource{}, "", err
	}
	resource, err := application.ParseOAuthResource(origin, origin+path)
	return resource, origin, err
}

func (h *Handler) mcpProtectedResource(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, protectedResourcePrefix)
	resource, origin, err := h.mcpResource(r, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"resource":                 resource.URI,
		"authorization_servers":    []string{origin},
		"scopes_supported":         []string{resource.Scope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Redlaunch MCP",
	})
}

func mcpChallenge(w http.ResponseWriter, resource application.OAuthResource, origin, code string) {
	path := strings.TrimPrefix(resource.URI, origin)
	value := "Bearer resource_metadata=" + strconv.Quote(origin+protectedResourcePrefix+path) +
		", scope=" + strconv.Quote(resource.Scope)
	if code != "" {
		value += ", error=" + strconv.Quote(code)
	}
	w.Header().Set("WWW-Authenticate", value)
}

// mcpHTTP authenticates the protected resource before constructing a request-
// scoped MCP server. Cookies do not authenticate this endpoint.
func (h *Handler) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil || h.apiTokens == nil {
		http.NotFound(w, r)
		return
	}
	resource, origin, err := h.mcpResource(r, r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Validate Origin on every method, including GET/DELETE (the general
	// browser middleware only checks unsafe methods).
	if candidate := r.Header.Get("Origin"); candidate != "" && candidate != origin {
		http.Error(w, "The request origin is not allowed.", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Only POST is supported.", http.StatusMethodNotAllowed)
		return
	}
	plaintext, ok := bearerToken(r)
	if !ok {
		mcpChallenge(w, resource, origin, "")
		writeAPIError(w, http.StatusUnauthorized, "OAuth Bearer authentication is required.")
		return
	}
	token, err := h.apiTokens.Authenticate(r.Context(), plaintext)
	if err != nil {
		if errors.Is(err, application.ErrAPITokenInvalid) || errors.Is(err, application.ErrAPITokenExpired) {
			mcpChallenge(w, resource, origin, "invalid_token")
			writeAPIError(w, http.StatusUnauthorized, "The OAuth access token is invalid or expired. Refresh it or sign in again.")
		} else {
			h.logger.Error("authenticate MCP token", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "The MCP credential could not be checked.")
		}
		return
	}
	if err := h.oauth.ValidateAccessTokenResource(r.Context(), plaintext, resource.URI); err != nil {
		var protocolError *application.OAuthError
		if !errors.As(err, &protocolError) {
			h.logger.Error("authorize MCP resource", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "The MCP credential could not be checked.")
			return
		}
		status, code := http.StatusForbidden, "insufficient_scope"
		if protocolError.Code == application.OAuthErrorInvalidGrant {
			status, code = http.StatusUnauthorized, "invalid_token"
		}
		mcpChallenge(w, resource, origin, code)
		writeAPIError(w, status, "This credential is not authorized for this MCP resource.")
		return
	}
	if token.Scope != resource.Scope || token.ApplicationID != resource.ApplicationID {
		mcpChallenge(w, resource, origin, "insufficient_scope")
		writeAPIError(w, http.StatusForbidden, "This credential does not have the required scope.")
		return
	}
	mux := http.NewServeMux()
	h.registerMachineAPIRoutes(mux)
	server, err := mcp.NewRemote(plaintext, resource.Scope == application.APITokenScopeManage, machineAPITransport{mux})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "MCP could not be configured.")
		return
	}
	server.ServeHTTP(w, r)
}

// machineAPITransport dispatches only to the fixed machine-API mux. It does
// not recurse through the outer middleware or expose a network destination.
type machineAPITransport struct{ handler http.Handler }

func (t machineAPITransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}
