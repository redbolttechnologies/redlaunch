package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/auth"
	"redlaunch/internal/store"
)

func newLocalLoginHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	hash, err := auth.HashPassword("test password")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateLocalUser(t.Context(), "admin@example.com", hash); err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32), CookieSecure: true}, database)
	if err != nil {
		t.Fatal(err)
	}
	web, err := New(nil, service)
	if err != nil {
		t.Fatal(err)
	}
	return web, database
}

func loginCSRF(t *testing.T, web *Handler) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.com/login?next=/help", nil))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	body := response.Body.String()
	for _, text := range []string{`autocomplete="username"`, `autocomplete="current-password"`, `method="post"`, `value="/help"`} {
		if !strings.Contains(body, text) {
			t.Fatal("missing login markup", text)
		}
	}
	if strings.Contains(body, "Login with Google") {
		t.Fatal("Google shown when disabled")
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			return cookie
		}
	}
	t.Fatal("no login CSRF cookie")
	return nil
}

func localLoginRequest(web *Handler, cookie *http.Cookie, username, password, target, origin string) *httptest.ResponseRecorder {
	form := url.Values{"username": {username}, "password": {password}, "next": {target}, "csrf_token": {cookie.Value}}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	return response
}

func TestLocalLoginHTTPAndLogout(t *testing.T) {
	web, database := newLocalLoginHandler(t)
	csrf := loginCSRF(t, web)
	response := localLoginRequest(web, csrf, "admin@example.com", "test password", "https://evil.example", "https://example.com")
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatal("unsafe redirect or failed login", response.Code)
	}
	var session *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe/missing session cookie")
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(session)
	user, valid, err := web.currentSessionUser(request)
	if err != nil || !valid || user.Email != "admin@example.com" || user.Username != "" {
		t.Fatal("local session identity invalid", err)
	}
	// The same CSRF-protected logout flow applies to both login methods.
	logout := httptest.NewRequest(http.MethodPost, "https://example.com/logout", strings.NewReader(url.Values{"csrf_token": {csrf.Value}}.Encode()))
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logout.AddCookie(csrf)
	logout.AddCookie(session)
	result := httptest.NewRecorder()
	web.Routes().ServeHTTP(result, logout)
	if result.Code != http.StatusSeeOther || result.Header().Get("Location") != "/login" {
		t.Fatal("logout failed", result.Code)
	}
	if err := database.ResetLocalPassword(t.Context(), "admin@example.com", "test reset hash"); err != nil {
		t.Fatal(err)
	}
	result = httptest.NewRecorder()
	web.Routes().ServeHTTP(result, request)
	if result.Code != http.StatusSeeOther || !strings.HasPrefix(result.Header().Get("Location"), "/login") {
		t.Fatal("revoked local session accepted")
	}
}

func TestLocalLoginHTTPFailures(t *testing.T) {
	web, _ := newLocalLoginHandler(t)
	csrf := loginCSRF(t, web)
	response := localLoginRequest(web, csrf, "admin@example.com", "incorrect password", "/help", "https://evil.example")
	if response.Code != http.StatusForbidden {
		t.Fatal("cross-origin login accepted")
	}
	badCookie := *csrf
	badCookie.Value = strings.Repeat("a", 64)
	form := url.Values{"username": {"admin@example.com"}, "password": {"test password"}, "csrf_token": {csrf.Value}}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&badCookie)
	response = httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("bad CSRF accepted")
	}
	for _, username := range []string{"admin@example.com", "missing", `<script>alert(1)</script>`} {
		response = localLoginRequest(web, csrf, username, "incorrect password", "/help", "")
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "Invalid email or password.") {
			t.Fatal("credential error differs", response.Code)
		}
		if strings.Contains(response.Body.String(), "incorrect password") || strings.Contains(response.Body.String(), "<script>alert(1)</script>") {
			t.Fatal("password disclosed or username unescaped")
		}
	}
	for range 4 {
		response = localLoginRequest(web, csrf, "admin@example.com", "incorrect password", "/", "")
	}
	response = localLoginRequest(web, csrf, "admin@example.com", "test password", "/", "")
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
		t.Fatal("login throttle missing", response.Code)
	}
	for _, path := range []string{"/auth/google", "/auth/google/callback"} {
		response = httptest.NewRecorder()
		web.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatal("disabled Google endpoint available")
		}
	}
}
