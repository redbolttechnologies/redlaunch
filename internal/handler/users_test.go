package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"redlaunch/internal/service"
	"redlaunch/internal/store"
	"strconv"
	"strings"
	"testing"
)

func usersTestHandler(t *testing.T) (*Handler, *store.Store, *http.Cookie, *http.Cookie) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	users, err := service.NewUserService(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Create(t.Context(), "admin@example.com", "test password"); err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32)}, database)
	if err != nil {
		t.Fatal(err)
	}
	web, err := New(nil, &fakeApplicationService{}, users, authentication)
	if err != nil {
		t.Fatal(err)
	}
	user, err := authentication.AuthenticatePassword(t.Context(), "admin@example.com", "test password", "ip")
	if err != nil {
		t.Fatal(err)
	}
	value, err := authentication.NewSession(user)
	if err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: sessionCookieName, Value: value}
	csrf := &http.Cookie{Name: csrfCookieName, Value: strings.Repeat("a", 64)}
	return web, database, session, csrf
}
func usersRequest(web *Handler, session, csrf *http.Cookie, method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		form.Set("csrf_token", csrf.Value)
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if session != nil {
		request.AddCookie(session)
	}
	if csrf != nil {
		request.AddCookie(csrf)
	}
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	return response
}
func TestSettingsUserManagement(t *testing.T) {
	web, database, session, csrf := usersTestHandler(t)
	page := usersRequest(web, session, csrf, http.MethodGet, "/settings?tab=users", nil)
	if page.Code != 200 {
		t.Fatal(page.Code)
	}
	for _, text := range []string{`id="users-tab"`, `id="users-panel"`, `admin@example.com`, `The last user cannot be deleted`, `data-user-dialog="delete"`} {
		if !strings.Contains(page.Body.String(), text) {
			t.Fatal("missing users UI", text)
		}
	}
	unauthorized := usersRequest(web, nil, csrf, http.MethodPost, "/settings/users", url.Values{"email": {"other@example.com"}, "password": {"second password"}, "password_confirmation": {"second password"}})
	if unauthorized.Code != http.StatusSeeOther {
		t.Fatal("unauthenticated user mutation accepted")
	}
	for _, form := range []url.Values{
		{"email": {"bad"}, "password": {"test password"}, "password_confirmation": {"test password"}},
		{"email": {"admin@example.com"}, "password": {"test password"}, "password_confirmation": {"test password"}},
		{"email": {"second@example.com"}, "password": {"test password"}, "password_confirmation": {"different password"}},
	} {
		result := usersRequest(web, session, csrf, http.MethodPost, "/settings/users", form)
		if result.Code < 400 || !strings.Contains(result.Body.String(), `id="user-create-dialog"`) {
			t.Fatal("validation failed", result.Code)
		}
		if strings.Contains(result.Body.String(), "test password") {
			t.Fatal("password echoed")
		}
	}
	result := usersRequest(web, session, csrf, http.MethodPost, "/settings/users", url.Values{"email": {"SECOND@EXAMPLE.COM"}, "password": {"second password"}, "password_confirmation": {"second password"}})
	if result.Code != 303 || result.Header().Get("Location") != "/settings?tab=users" {
		t.Fatal("create failed", result.Code)
	}
	second, err := database.GetLocalUser(t.Context(), "second@example.com")
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(second.ID, 10)
	result = usersRequest(web, session, csrf, http.MethodPost, "/settings/users/password", url.Values{"id": {id}, "password": {"updated password"}, "password_confirmation": {"updated password"}})
	if result.Code != 303 {
		t.Fatal("reset failed", result.Code)
	}
	result = usersRequest(web, session, csrf, http.MethodPost, "/settings/users/delete", url.Values{"id": {id}})
	if result.Code != 400 {
		t.Fatal("deletion not confirmed")
	}
	result = usersRequest(web, session, csrf, http.MethodPost, "/settings/users/delete", url.Values{"id": {id}, "confirm": {"yes"}})
	if result.Code != 303 {
		t.Fatal("delete failed", result.Code)
	}
	admin, err := database.GetLocalUser(t.Context(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	result = usersRequest(web, session, csrf, http.MethodPost, "/settings/users/delete", url.Values{"id": {strconv.FormatInt(admin.ID, 10)}, "confirm": {"yes"}})
	if result.Code != 409 || !strings.Contains(result.Body.String(), "The last user cannot be deleted") {
		t.Fatal("last user deleted", result.Code)
	}
}
func TestUsersCSRFAndSelfPasswordChange(t *testing.T) {
	web, database, session, csrf := usersTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/settings/users", strings.NewReader("email=x@example.com"))
	request.AddCookie(session)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	request = httptest.NewRequest(http.MethodPost, "http://example.com/settings/users", strings.NewReader(url.Values{"csrf_token": {csrf.Value}}.Encode()))
	request.AddCookie(session)
	request.AddCookie(csrf)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	admin, err := database.GetLocalUser(t.Context(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	response = usersRequest(web, session, csrf, http.MethodPost, "/settings/users/password", url.Values{"id": {strconv.FormatInt(admin.ID, 10)}, "password": {"updated password"}, "password_confirmation": {"updated password"}})
	if response.Code != 303 || response.Header().Get("Location") != "/login" {
		t.Fatal("self reset did not sign out", response.Code)
	}
	response = usersRequest(web, session, csrf, http.MethodGet, "/settings?tab=users", nil)
	if response.Code != 303 {
		t.Fatal("old session valid")
	}
}

func TestLegacyUserCanSetEmailWithoutChangingPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	hash, err := auth.HashPassword("legacy password")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Model an installed instance before the shared-user migration.
	if _, err = fixture.Exec(`DROP TABLE users; DELETE FROM schema_migrations WHERE version=24;`); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(`INSERT INTO local_users(username,password_hash,created_at,updated_at) VALUES('admin',?,'2026-01-01','2026-01-01')`, hash); err != nil {
		t.Fatal(err)
	}
	fixture.Close()
	database, err = store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	users, err := service.NewUserService(database)
	if err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.New(auth.Config{SessionSecret: strings.Repeat("s", 32)}, database)
	if err != nil {
		t.Fatal(err)
	}
	web, err := New(nil, &fakeApplicationService{}, users, authentication)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := authentication.AuthenticatePassword(t.Context(), "admin", "legacy password", "ip")
	if err != nil {
		t.Fatal("legacy sign-in lost", err)
	}
	value, err := authentication.NewSession(legacy)
	if err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: sessionCookieName, Value: value}
	csrf := &http.Cookie{Name: csrfCookieName, Value: strings.Repeat("a", 64)}
	page := usersRequest(web, session, csrf, http.MethodGet, "/settings?tab=users", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Set email") || !strings.Contains(page.Body.String(), "Email required") {
		t.Fatal("legacy UI missing", page.Code)
	}
	response := usersRequest(web, session, csrf, http.MethodPost, "/settings/users/email", url.Values{"id": {strconv.FormatInt(legacy.AccountID, 10)}, "email": {"ADMIN@EXAMPLE.COM"}})
	if response.Code != 303 || response.Header().Get("Location") != "/login" {
		t.Fatal("email assignment failed", response.Code)
	}
	if _, valid, _ := authentication.ValidateSession(t.Context(), value); valid {
		t.Fatal("email assignment did not revoke session")
	}
	if _, err := authentication.AuthenticatePassword(t.Context(), "admin", "legacy password", "ip"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatal("old username still works", err)
	}
	if _, err := authentication.AuthenticatePassword(t.Context(), "admin@example.com", "legacy password", "ip"); err != nil {
		t.Fatal("password changed during assignment", err)
	}
	if _, err := database.GetLocalUser(t.Context(), "admin"); !errors.Is(err, application.ErrUserNotFound) {
		t.Fatal(err)
	}
	allowed, err := database.IsAuthorizedEmail(t.Context(), "admin@example.com")
	if err != nil || !allowed {
		t.Fatal("Google membership not assigned", err)
	}
}
