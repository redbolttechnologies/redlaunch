package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

type fakeServerSSHKeyService struct {
	keys        []application.ServerSSHKey
	nextID      int64
	privateKey  string
	createErr   error
	createInput application.ServerSSHKeyInput
	revokeID    int64
	revokeErr   error
}

func newFakeServerSSHKeyService() *fakeServerSSHKeyService {
	return &fakeServerSSHKeyService{nextID: 1}
}

func (s *fakeServerSSHKeyService) Username() string { return "redlaunch" }
func (s *fakeServerSSHKeyService) List(context.Context) ([]application.ServerSSHKey, error) {
	return append([]application.ServerSSHKey(nil), s.keys...), nil
}
func (s *fakeServerSSHKeyService) Create(_ context.Context, input application.ServerSSHKeyInput) (application.ServerSSHKeySetup, error) {
	s.createInput = input
	if s.createErr != nil {
		return application.ServerSSHKeySetup{}, s.createErr
	}
	normalized, err := application.ValidateSSHKeyDisplayName(input.DisplayName)
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}
	key := application.ServerSSHKey{
		ID:             s.nextID,
		DisplayName:    normalized,
		PublicKey:      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMfake redlaunch-ssh-key",
		KeyFingerprint: "SHA256:fake",
		ApplicationID:  input.ApplicationID,
		ServiceName:    input.ServiceName,
	}
	if input.ApplicationID != 0 {
		key.PermitOpen = "127.0.0.1:5432"
	}
	s.nextID++
	s.keys = append(s.keys, key)
	privateKey := s.privateKey
	if privateKey == "" {
		privateKey = "private-key-material"
	}
	return application.ServerSSHKeySetup{Key: key, PrivateKey: privateKey}, nil
}
func (s *fakeServerSSHKeyService) Revoke(_ context.Context, id int64) error {
	s.revokeID = id
	if s.revokeErr != nil {
		return s.revokeErr
	}
	for index, key := range s.keys {
		if key.ID == id {
			s.keys = append(s.keys[:index], s.keys[index+1:]...)
			return nil
		}
	}
	return application.ErrSSHKeyNotFound
}

func newSettingsHandlerWithSSH(t *testing.T, applications *fakeApplicationService, sshKeys *fakeServerSSHKeyService) *Handler {
	t.Helper()
	dependencies := []any{applications}
	if sshKeys != nil {
		dependencies = append(dependencies, sshKeys)
	}
	web, err := New(nil, dependencies...)
	if err != nil {
		t.Fatal(err)
	}
	return web
}

func TestSettingsPageRendersSSHKeysTab(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	sshKeys.keys = []application.ServerSSHKey{{ID: 1, DisplayName: "GHA migrator workflow access", PublicKey: "ssh-ed25519 AAAA", KeyFingerprint: "SHA256:abc"}}
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="ssh-keys-tab"`,
		`id="ssh-keys-panel"`,
		`data-ssh-key-add`,
		`id="ssh-key-create-dialog"`,
		`data-ssh-key-create-dialog`,
		`action="/settings/ssh-keys"`,
		`action="/settings/ssh-keys/delete"`,
		`GHA migrator workflow access`,
		`SHA256:abc`,
		`/static/ssh-keys.js`,
		`name="display_name"`,
		`redlaunch`,
		`service-widget-body settings-ssh-keys-body`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /settings did not render %q", expected)
		}
	}
	// NOTE: intentionally split so this assertion itself never matches the
	// secret-scan for tracked private-key material.
	if strings.Contains(body, "OPENSSH PRIVATE"+" KEY") || strings.Contains(body, "private-key-material") {
		t.Fatalf("GET /settings rendered private key material without creation")
	}
}

func TestSettingsPageRendersServiceRestrictionPicker(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "db"}, {ID: 10, ApplicationID: 7, Name: "web"}},
	}
	web := newSettingsHandlerWithSSH(t, applications, sshKeys)

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`name="service_ref"`,
		`Full shell access (unrestricted)`,
		`value="7/db"`,
		`Status page / db`,
		`Status page / web`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("GET /settings did not render service picker %q", expected)
		}
	}
}

func TestCreateServerSSHKeyShowsPrivateKeyOnce(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	sshKeys.privateKey = "private-key-one-time"
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{"csrf_token": {web.csrfToken}, "display_name": {"GHA migrator workflow access"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /settings/ssh-keys status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"will not be shown again",
		"private-key-one-time",
		`id="ssh-key-setup-dialog"`,
		`data-ssh-key-setup-dialog`,
		`data-ssh-key-setup-open`,
		`id="ssh-key-private-value"`,
		`data-copy-target="ssh-key-private-value"`,
		`data-ssh-key-download`,
		`GHA migrator workflow access`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("POST /settings/ssh-keys did not render one-time handoff %q", expected)
		}
	}
	if sshKeys.createInput.DisplayName != "GHA migrator workflow access" {
		t.Fatalf("create display name = %q, want raw input", sshKeys.createInput.DisplayName)
	}
	if sshKeys.createInput.ApplicationID != 0 || sshKeys.createInput.ServiceName != "" {
		t.Fatalf("unrestricted create input = %+v, want no restriction", sshKeys.createInput)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSH key handoff Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}

	followUp := httptest.NewRecorder()
	web.Routes().ServeHTTP(followUp, httptest.NewRequest(http.MethodGet, "/settings?tab=ssh-keys", nil))
	if followUp.Code != http.StatusOK {
		t.Fatalf("GET /settings after creation status = %d, want %d", followUp.Code, http.StatusOK)
	}
	if strings.Contains(followUp.Body.String(), "private-key-one-time") {
		t.Fatalf("private key persisted beyond the creation response")
	}
}

func TestCreateServerSSHKeyWithServiceRestriction(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "db"}},
	}
	web := newSettingsHandlerWithSSH(t, applications, sshKeys)

	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"display_name": {"migrator access"},
		"service_ref":  {"7/db"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /settings/ssh-keys status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if sshKeys.createInput.ApplicationID != 7 || sshKeys.createInput.ServiceName != "db" {
		t.Fatalf("create restriction = %+v, want application 7 service db", sshKeys.createInput)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="ssh-key-tunnel-usage"`,
		`ssh -N -L 127.0.0.1:5432:127.0.0.1:5432`,
		`Tunnel only:`,
		`127.0.0.1:5432`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("restricted key handoff did not render %q", expected)
		}
	}
}

func TestCreateServerSSHKeyRejectsMalformedServiceRef(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"display_name": {"migrator access"},
		"service_ref":  {"bogus"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST malformed service_ref status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Select a service") {
		t.Fatalf("malformed service_ref did not render picker error: %s", body)
	}
	if len(sshKeys.keys) != 0 {
		t.Fatalf("malformed service_ref reached the key service")
	}
}

func TestCreateServerSSHKeyRendersNoTargetPortError(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	sshKeys.createErr = application.ErrSSHKeyServiceHasNoTargetPort
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
		services:     []application.Service{{ID: 9, ApplicationID: 7, Name: "db"}},
	}
	web := newSettingsHandlerWithSSH(t, applications, sshKeys)

	form := url.Values{
		"csrf_token":   {web.csrfToken},
		"display_name": {"migrator access"},
		"service_ref":  {"7/db"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST unrestrictable service status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"publishes no host port",
		`value="7/db" selected`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("unrestrictable service did not render %q", expected)
		}
	}
}

func TestCreateServerSSHKeyRendersValidationError(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{"csrf_token": {web.csrfToken}, "display_name": {"   "}}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid SSH key status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Enter a display name") {
		t.Fatalf("invalid SSH key did not render field error: %s", body)
	}
	if !strings.Contains(body, `application-alert application-dialog-alert`) {
		t.Fatalf("SSH key error did not use the in-form alert pattern: %s", body)
	}
	for _, expected := range []string{
		`id="ssh-key-create-dialog"`,
		`data-ssh-key-create-dialog`,
		`data-ssh-key-create-open`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSH key error did not reopen the create dialog %q: %s", expected, body)
		}
	}
	alertIndex := strings.Index(body, `application-dialog-alert`)
	formIndex := strings.Index(body, `action="/settings/ssh-keys"`)
	fieldIndex := strings.Index(body, `id="ssh-key-display-name"`)
	dialogIndex := strings.Index(body, `data-ssh-key-create-dialog`)
	if alertIndex < 0 || formIndex < 0 || fieldIndex < 0 || dialogIndex < 0 || !(dialogIndex < formIndex && formIndex < alertIndex && alertIndex < fieldIndex) {
		t.Fatalf("SSH key error is not placed inside the create dialog form above its fields: %s", body)
	}
	if len(sshKeys.keys) != 0 {
		t.Fatalf("invalid SSH key was stored: %#v", sshKeys.keys)
	}
}

func TestCreateServerSSHKeyRequiresCSRF(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{"display_name": {"GHA access"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST /settings/ssh-keys without CSRF status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if len(sshKeys.keys) != 0 {
		t.Fatalf("SSH key without CSRF reached service")
	}
}

func TestRevokeServerSSHKeyRedirects(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	sshKeys.keys = []application.ServerSSHKey{{ID: 3, DisplayName: "old key"}}
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{"csrf_token": {web.csrfToken}, "id": {"3"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/ssh-keys/delete status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	if got := recorder.Header().Get("Location"); got != "/settings?tab=ssh-keys" {
		t.Fatalf("revoke Location = %q, want /settings?tab=ssh-keys", got)
	}
	if sshKeys.revokeID != 3 || len(sshKeys.keys) != 0 {
		t.Fatalf("revoke did not remove key: %#v", sshKeys.keys)
	}
}

func TestRevokeServerSSHKeyRendersNotFound(t *testing.T) {
	sshKeys := newFakeServerSSHKeyService()
	sshKeys.revokeErr = errors.New("wrapped: " + application.ErrSSHKeyNotFound.Error())
	// Ensure errors.Is works through wrapping.
	sshKeys.revokeErr = errors.Join(sshKeys.revokeErr, application.ErrSSHKeyNotFound)
	web := newSettingsHandlerWithSSH(t, &fakeApplicationService{}, sshKeys)

	form := url.Values{"csrf_token": {web.csrfToken}, "id": {"99"}}
	request := httptest.NewRequest(http.MethodPost, "/settings/ssh-keys/delete", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: web.csrfToken})
	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("POST missing SSH key status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "could not be found") {
		t.Fatalf("missing SSH key did not render not-found error: %s", body)
	}
}
