// Package handler contains the server-rendered HTTP interface.
package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const csrfCookieName = "redlaunch_csrf"

const (
	oauthStateCookieName    = "redlaunch_oauth_state"
	oauthRedirectCookieName = "redlaunch_oauth_redirect"
	sessionCookieName       = "redlaunch_session"
	oauthCallbackPath       = "/auth/google/callback"
)

func (h *Handler) withCSRFProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || !validCSRFTokenFormat(cookie.Value) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "This request requires a fresh page token.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) withOriginCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			referer := strings.TrimSpace(r.Header.Get("Referer"))
			if referer != "" {
				origin = refererOrigin(referer)
				if origin == "" {
					w.Header().Set("Cache-Control", "no-store")
					http.Error(w, "The request origin is not allowed.", http.StatusForbidden)
					return
				}
			}
		}
		if origin != "" && !h.sameRequestOrigin(r, origin) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "The request origin is not allowed.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) sameRequestOrigin(r *http.Request, candidate string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != h.requestScheme(r) {
		return false
	}
	requestHost := strings.TrimSpace(r.Host)
	if requestHost == "" && r.URL != nil {
		requestHost = strings.TrimSpace(r.URL.Host)
	}
	requestScheme := h.requestScheme(r)
	return canonicalRequestHost(requestHost, requestScheme) != "" && canonicalRequestHost(requestHost, requestScheme) == canonicalRequestHost(parsed.Host, parsed.Scheme)
}

func (h *Handler) requestScheme(r *http.Request) string {
	if r.TLS != nil || h.cookieSecure || h.accessMode == accessModeManagedHTTPS {
		return "https"
	}
	return "http"
}

func canonicalRequestHost(value, scheme string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		if (scheme == "http" && port == "80" || scheme == "https" && port == "443") && host != "" {
			return strings.TrimSuffix(host, ".")
		}
		return strings.TrimSuffix(host, ".") + ":" + port
	}
	return strings.TrimSuffix(value, ".")
}

func refererOrigin(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

func (h *Handler) secureCookie(r *http.Request) bool {
	return r.TLS != nil || h.cookieSecure || h.accessMode == accessModeManagedHTTPS || h.authentication != nil && h.authentication.CookieSecure()
}

func (h *Handler) csrfTokenForRequest(r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && validCSRFTokenFormat(cookie.Value) {
		return cookie.Value
	}
	return h.csrfToken
}

func (h *Handler) validRequestCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	return err == nil && validCSRFTokenFormat(cookie.Value) && validCSRFToken(r.Form.Get("csrf_token"), cookie.Value)
}

func (h *Handler) setCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	token := h.csrfTokenForRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   h.secureCookie(r),
	})
	return token
}

func (h *Handler) expireOAuthCookies(w http.ResponseWriter, r *http.Request) {
	secure := h.secureCookie(r)
	for _, name := range []string{oauthStateCookieName, oauthRedirectCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
		})
	}
}

func (h *Handler) expireSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.secureCookie(r),
	})
}

func (h *Handler) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https://googleusercontent.com https://*.googleusercontent.com; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func newCSRFToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func validCSRFToken(got, want string) bool {
	if !validCSRFTokenFormat(got) || !validCSRFTokenFormat(want) || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func validCSRFTokenFormat(token string) bool {
	if token == "" || len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}
