// Package handler contains the server-rendered HTTP interface.
package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"redlaunch/internal/application"
	redlaunchauth "redlaunch/internal/auth"
)

type authenticatedUserContextKey struct{}

func (h *Handler) withAuthentication(next http.Handler) http.Handler {
	if !h.authenticationEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAuthenticationPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			h.redirectToLogin(w, r, r.URL.RequestURI())
			return
		}
		user, valid, err := h.authentication.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			h.logger.Error("validate authentication session", "error", err)
			http.Error(w, "The authentication state could not be checked.", http.StatusInternalServerError)
			return
		}
		if !valid {
			h.expireSessionCookie(w, r)
			h.redirectToLogin(w, r, r.URL.RequestURI())
			return
		}
		requestContext := context.WithValue(r.Context(), authenticatedUserContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(requestContext))
	})
}

func isPublicAuthenticationPath(path string) bool {
	return path == "/login" ||
		path == "/auth/google" ||
		path == "/auth/google/callback" ||
		path == "/healthz" ||
		strings.HasPrefix(path, "/static/")
}

func (h *Handler) authenticationEnabled() bool {
	return h.authentication != nil && h.authentication.Enabled()
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	if _, valid, err := h.currentSessionUser(r); err != nil {
		h.logger.Error("check existing authentication session", "error", err)
		http.Error(w, "The authentication state could not be checked.", http.StatusInternalServerError)
		return
	} else if valid {
		http.Redirect(w, r, safeRedirectTarget(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}

	errorMessage := loginErrorMessage(r.URL.Query().Get("error"))
	target := safeRedirectTarget(r.URL.Query().Get("next"))
	h.writeLoginPage(w, http.StatusOK, loginPageData{
		Error:          errorMessage,
		GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
	})
}

func (h *Handler) googleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	redirectURL, err := h.oauthRedirectURL(r)
	if err != nil {
		h.logger.Error("resolve Google OAuth redirect URL", "error", err)
		http.Error(w, "Google sign-in could not be started.", http.StatusInternalServerError)
		return
	}
	state, err := newCSRFToken()
	if err != nil {
		h.logger.Error("create Google OAuth state", "error", err)
		http.Error(w, "Google sign-in could not be started.", http.StatusInternalServerError)
		return
	}
	target := safeRedirectTarget(r.URL.Query().Get("next"))
	secure := h.secureCookie(r)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   10 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     oauthRedirectCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(target)),
		Path:     "/",
		MaxAge:   10 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	http.Redirect(w, r, h.authorizationURL(state, redirectURL), http.StatusFound)
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}
	target := "/"
	if cookie, err := r.Cookie(oauthRedirectCookieName); err == nil {
		if decoded, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value); decodeErr == nil {
			target = safeRedirectTarget(string(decoded))
		}
	}
	h.expireOAuthCookies(w, r)

	stateCookie, err := r.Cookie(oauthStateCookieName)
	if err != nil || !validCSRFToken(r.URL.Query().Get("state"), stateCookie.Value) {
		h.writeLoginPage(w, http.StatusBadRequest, loginPageData{
			Error:          "The Google sign-in session expired. Start again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	if r.URL.Query().Get("error") != "" {
		h.redirectToLoginWithError(w, r, target, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.writeLoginPage(w, http.StatusBadRequest, loginPageData{
			Error:          "Google did not return an authorization code. Start again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}

	redirectURL, err := h.oauthRedirectURL(r)
	if err != nil {
		h.logger.Error("resolve Google OAuth redirect URL", "error", err)
		http.Error(w, "Google sign-in could not be completed.", http.StatusInternalServerError)
		return
	}
	user, err := h.completeLogin(r.Context(), code, redirectURL)
	if errors.Is(err, redlaunchauth.ErrNotAuthorized) || errors.Is(err, redlaunchauth.ErrEmailNotVerified) || errors.Is(err, application.ErrEmailInvalid) {
		h.redirectToLoginWithError(w, r, target, "unauthorized")
		return
	}
	if err != nil {
		h.logger.Error("complete Google login", "error", err)
		h.writeLoginPage(w, http.StatusBadGateway, loginPageData{
			Error:          "Google sign-in could not be completed. Try again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	session, err := h.authentication.NewSession(user)
	if err != nil {
		h.logger.Error("create authentication session", "error", err)
		h.writeLoginPage(w, http.StatusInternalServerError, loginPageData{
			Error:          "Your sign-in could not be saved. Try again.",
			GoogleLoginURL: "/auth/google?next=" + url.QueryEscape(target),
		})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session,
		Path:     "/",
		MaxAge:   int(h.authentication.SessionDuration().Seconds()),
		Expires:  time.Now().Add(h.authentication.SessionDuration()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.secureCookie(r),
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Handler) currentSessionUser(r *http.Request) (redlaunchauth.User, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return redlaunchauth.User{}, false, nil
	}
	return h.authentication.ValidateSession(r.Context(), cookie.Value)
}

func (h *Handler) authorizationURL(state, redirectURL string) string {
	if authentication, ok := h.authentication.(authenticationRedirectService); ok {
		return authentication.AuthorizationURLForRedirect(state, redirectURL)
	}
	return h.authentication.AuthorizationURL(state)
}

func (h *Handler) completeLogin(ctx context.Context, code, redirectURL string) (redlaunchauth.User, error) {
	if authentication, ok := h.authentication.(authenticationRedirectService); ok {
		return authentication.CompleteLoginForRedirect(ctx, code, redirectURL)
	}
	return h.authentication.CompleteLogin(ctx, code)
}

func (h *Handler) oauthRedirectURL(r *http.Request) (string, error) {
	if _, ok := h.authentication.(authenticationRedirectService); !ok {
		return "", nil
	}
	if h.redlaunchDomains == nil {
		return "", nil
	}
	domains, err := h.redlaunchDomains.ListRedlaunchDomains(r.Context())
	if err != nil {
		return "", fmt.Errorf("list Redlaunch domains: %w", err)
	}
	for _, domain := range domains {
		name, err := application.ValidateDomainName(domain.Name)
		if err != nil {
			continue
		}
		if requestUsesPublicHost(r, name) {
			return (&url.URL{Scheme: "https", Host: name, Path: oauthCallbackPath}).String(), nil
		}
	}
	return "", nil
}

func requestUsesPublicHost(r *http.Request, expectedDomain string) bool {
	host := strings.TrimSpace(r.Host)
	if host == "" && r.URL != nil {
		host = strings.TrimSpace(r.URL.Host)
	}
	if host == "" {
		return false
	}
	parsed, err := url.Parse("//" + host)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	expectedDomain = strings.TrimSuffix(strings.ToLower(expectedDomain), ".")
	return hostname != "" && hostname == expectedDomain
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.authenticationEnabled() {
		http.NotFound(w, r)
		return
	}

	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The logout request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This logout request expired. Refresh the page and try again.", http.StatusForbidden)
		return
	}

	h.expireSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request, target string) {
	h.redirectToLoginWithError(w, r, target, "")
}

func (h *Handler) redirectToLoginWithError(w http.ResponseWriter, r *http.Request, target, errorCode string) {
	values := url.Values{"next": {safeRedirectTarget(target)}}
	if errorCode != "" {
		values.Set("error", errorCode)
	}
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusSeeOther)
}

func loginErrorMessage(code string) string {
	switch code {
	case "cancelled":
		return "Google sign-in was cancelled."
	case "unauthorized":
		return "This Google account is not authorized to use Redlaunch."
	case "oauth":
		return "Google sign-in could not be completed. Try again."
	default:
		return ""
	}
}

func safeRedirectTarget(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") {
		return "/"
	}
	return parsed.RequestURI()
}
