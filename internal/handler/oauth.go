package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"redlaunch/internal/application"
)

// oauthService is the authorization server behind the /oauth endpoints and
// the authorization server metadata document.
type oauthService interface {
	ListConnections(context.Context) ([]application.OAuthConnection, error)
	RevokeConnection(context.Context, int64) error
	RegisterClient(context.Context, application.OAuthClientRegistration) (application.OAuthClient, error)
	ValidateAuthorization(context.Context, string, application.OAuthAuthorizationRequest) (application.OAuthAuthorization, error)
	CompleteAuthorization(context.Context, string, application.OAuthAuthorizationRequest, bool) (string, error)
	ExchangeToken(context.Context, application.OAuthTokenRequest) (application.OAuthTokenResponse, error)
	RevokeToken(context.Context, string) error
	ValidateAccessTokenResource(context.Context, string, string) error
}

const maxOAuthRegistrationBody = 16 << 10

// isOAuthMachinePath lists the endpoints MCP clients call without a browser
// session or CSRF cookie. Authorization and consent stay browser-protected.
func isOAuthMachinePath(path string) bool {
	if isMCPMachinePath(path) {
		return true
	}
	switch path {
	case "/.well-known/oauth-authorization-server", "/oauth/register", "/oauth/token", "/oauth/revoke":
		return true
	}
	return false
}

// oauthAuthorizationEnabled gates the browser authorization pages. They must
// never run without a signed-in owner, so they require authentication.
func (h *Handler) oauthAuthorizationEnabled() bool {
	return h.oauth != nil && h.authenticationEnabled()
}

// oauthMetadata publishes the authorization server metadata (RFC 8414).
func (h *Handler) oauthMetadata(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		http.NotFound(w, r)
		return
	}
	origin, err := h.oauthOrigin(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"issuer":                                     origin,
		"authorization_endpoint":                     origin + "/oauth/authorize",
		"token_endpoint":                             origin + "/oauth/token",
		"registration_endpoint":                      origin + "/oauth/register",
		"revocation_endpoint":                        origin + "/oauth/revoke",
		"scopes_supported":                           []string{application.APITokenScopeManage, application.APITokenScopeRun},
		"response_types_supported":                   []string{application.OAuthResponseTypeCode},
		"grant_types_supported":                      []string{application.OAuthGrantTypeAuthorizationCode, application.OAuthGrantTypeRefreshToken},
		"code_challenge_methods_supported":           []string{application.OAuthCodeChallengeMethodS256},
		"token_endpoint_auth_methods_supported":      []string{"none"},
		"revocation_endpoint_auth_methods_supported": []string{"none"},
	})
}

type oauthRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// oauthRegister implements dynamic client registration (RFC 7591).
func (h *Handler) oauthRegister(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthRegistrationBody)
	var request oauthRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeOAuthError(w, http.StatusBadRequest, application.OAuthErrorInvalidClientMetadata, "The registration request must be a JSON object.")
		return
	}
	client, err := h.oauth.RegisterClient(r.Context(), application.OAuthClientRegistration{
		ClientName:              request.ClientName,
		RedirectURIs:            request.RedirectURIs,
		GrantTypes:              request.GrantTypes,
		ResponseTypes:           request.ResponseTypes,
		TokenEndpointAuthMethod: request.TokenEndpointAuthMethod,
	})
	if err != nil {
		h.writeOAuthServiceError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.Name,
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                []string{application.OAuthGrantTypeAuthorizationCode, application.OAuthGrantTypeRefreshToken},
		"response_types":             []string{application.OAuthResponseTypeCode},
		"token_endpoint_auth_method": "none",
	})
}

// oauthAuthorize shows the owner what the client is asking for. The session
// middleware sends anonymous visitors to sign-in first and returns them here.
func (h *Handler) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if !h.oauthAuthorizationEnabled() {
		http.NotFound(w, r)
		return
	}
	origin, err := h.oauthOrigin(r)
	if err != nil {
		h.writeOAuthPage(w, http.StatusBadRequest, "This sign-in request was sent to an address this server does not serve.")
		return
	}
	request := oauthAuthorizationRequestFromValues(r.URL.Query())
	authorization, err := h.oauth.ValidateAuthorization(r.Context(), origin, request)
	if err != nil {
		h.writeOAuthAuthorizationError(w, err)
		return
	}
	h.writeOAuthConsent(w, r, authorization, request)
}

// oauthAuthorizeDecision records the owner's approval or denial and sends the
// browser back to the client's redirect URI.
func (h *Handler) oauthAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	if !h.oauthAuthorizationEnabled() {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		h.writeOAuthPage(w, http.StatusBadRequest, "The sign-in request was invalid.")
		return
	}
	if !h.validRequestCSRF(r) {
		h.writeOAuthPage(w, http.StatusForbidden, "This approval page expired. Start the connection again from your assistant.")
		return
	}
	origin, err := h.oauthOrigin(r)
	if err != nil {
		h.writeOAuthPage(w, http.StatusBadRequest, "This sign-in request was sent to an address this server does not serve.")
		return
	}
	request := oauthAuthorizationRequestFromValues(r.Form)
	location, err := h.oauth.CompleteAuthorization(r.Context(), origin, request, r.Form.Get("decision") == "approve")
	if err != nil {
		h.writeOAuthAuthorizationError(w, err)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// oauthToken exchanges an authorization code or refresh token (RFC 6749 section 4).
func (h *Handler) oauthToken(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		writeOAuthError(w, http.StatusBadRequest, application.OAuthErrorInvalidRequest, "The token request must be form-encoded.")
		return
	}
	response, err := h.oauth.ExchangeToken(r.Context(), application.OAuthTokenRequest{
		GrantType:    r.Form.Get("grant_type"),
		ClientID:     r.Form.Get("client_id"),
		Code:         r.Form.Get("code"),
		RedirectURI:  r.Form.Get("redirect_uri"),
		CodeVerifier: r.Form.Get("code_verifier"),
		RefreshToken: r.Form.Get("refresh_token"),
		Resource:     r.Form.Get("resource"),
	})
	if err != nil {
		h.writeOAuthServiceError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"access_token":  response.AccessToken,
		"token_type":    response.TokenType,
		"expires_in":    response.ExpiresIn,
		"refresh_token": response.RefreshToken,
		"scope":         response.Scope,
	})
}

// oauthRevoke ends the grant behind a token (RFC 7009).
func (h *Handler) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if h.oauth == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		writeOAuthError(w, http.StatusBadRequest, application.OAuthErrorInvalidRequest, "The revocation request must be form-encoded.")
		return
	}
	token := r.Form.Get("token")
	if token == "" {
		writeOAuthError(w, http.StatusBadRequest, application.OAuthErrorInvalidRequest, "token is required.")
		return
	}
	if err := h.oauth.RevokeToken(r.Context(), token); err != nil {
		h.logger.Error("revoke OAuth grant", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, application.OAuthErrorServerError, "The token could not be revoked.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// oauthOrigin returns the public origin the request arrived on. Loopback
// addresses (local use and SSH tunnels) are accepted as-is. Any other host
// must be a configured Redlaunch domain reached over HTTPS, so tokens are never
// bound to an arbitrary Host header.
func (h *Handler) oauthOrigin(r *http.Request) (string, error) {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	parsed, err := url.Parse("//" + host)
	if host == "" || err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return "", errors.New("request host is invalid")
	}
	if application.IsLoopbackHost(parsed.Hostname()) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + host, nil
	}
	if h.requestScheme(r) != "https" || h.redlaunchDomains == nil {
		return "", errors.New("request host must be HTTPS")
	}
	domains, err := h.redlaunchDomains.ListRedlaunchDomains(r.Context())
	if err != nil {
		return "", fmt.Errorf("list Redlaunch domains: %w", err)
	}
	for _, domain := range domains {
		name, err := application.ValidateDomainName(domain.Name)
		if err == nil && requestUsesPublicHost(r, name) {
			return "https://" + name, nil
		}
	}
	return "", errors.New("request host is not a configured Redlaunch domain")
}

func oauthAuthorizationRequestFromValues(values url.Values) application.OAuthAuthorizationRequest {
	return application.OAuthAuthorizationRequest{
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		ResponseType:        values.Get("response_type"),
		State:               values.Get("state"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Resource:            values.Get("resource"),
		Scope:               values.Get("scope"),
	}
}

type oauthConsentPageData struct {
	CSRFToken     string
	ClientName    string
	RedirectHost  string
	ResourceLabel string
	Request       application.OAuthAuthorizationRequest
}

type oauthErrorPageData struct {
	Message string
}

func (h *Handler) writeOAuthConsent(w http.ResponseWriter, r *http.Request, authorization application.OAuthAuthorization, request application.OAuthAuthorizationRequest) {
	data := oauthConsentPageData{
		CSRFToken:     h.setCSRFCookie(w, r),
		ClientName:    authorization.Client.Name,
		RedirectHost:  redirectURIHost(authorization.RedirectURI),
		ResourceLabel: oauthResourceLabel(authorization),
		Request:       request,
	}
	h.writeOAuthTemplate(w, http.StatusOK, "oauth-consent.html", data)
}

func (h *Handler) writeOAuthPage(w http.ResponseWriter, status int, message string) {
	h.writeOAuthTemplate(w, status, "oauth-error.html", oauthErrorPageData{Message: message})
}

func (h *Handler) writeOAuthTemplate(w http.ResponseWriter, status int, name string, data any) {
	var body bytes.Buffer
	if err := h.templates.ExecuteTemplate(&body, name, data); err != nil {
		h.logger.Error("render OAuth template", "template", name, "error", err)
		http.Error(w, "The page could not be rendered.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (h *Handler) writeOAuthAuthorizationError(w http.ResponseWriter, err error) {
	var protocolError *application.OAuthError
	if errors.As(err, &protocolError) {
		h.writeOAuthPage(w, http.StatusBadRequest, protocolError.Description)
		return
	}
	h.logger.Error("OAuth authorization failed", "error", err)
	h.writeOAuthPage(w, http.StatusInternalServerError, "The connection could not be approved. Try again.")
}

func (h *Handler) writeOAuthServiceError(w http.ResponseWriter, err error) {
	var protocolError *application.OAuthError
	if !errors.As(err, &protocolError) {
		h.logger.Error("OAuth request failed", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, application.OAuthErrorServerError, "The authorization server could not complete the request.")
		return
	}
	status := http.StatusBadRequest
	if protocolError.Code == application.OAuthErrorTemporarilyUnavailable {
		status = http.StatusServiceUnavailable
	}
	writeOAuthError(w, status, protocolError.Code, protocolError.Description)
}

// writeOAuthError renders an RFC 6749 section 5.2 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Pragma", "no-cache")
	writeAPIJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func oauthResourceLabel(authorization application.OAuthAuthorization) string {
	if authorization.Resource.Scope == application.APITokenScopeManage {
		return "Manage this server: applications, services, environment variables, domains, and databases."
	}
	return "Run services in " + authorization.ApplicationName + " and check their progress."
}

func redirectURIHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Host
}
