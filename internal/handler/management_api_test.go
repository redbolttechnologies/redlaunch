package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func managementRequest(web *Handler, operation, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/management/"+operation, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, req)
	return response
}

func managementTestHandler(t *testing.T, apps *fakeApplicationService, db *fakeManagedDatabases) (*Handler, *fakeAPITokenService) {
	t.Helper()
	tokens := newFakeAPITokenService()
	tokens.authToken = application.APIToken{ID: 5, Scope: application.APITokenScopeManage}
	dependencies := []any{apps, tokens}
	if db != nil {
		dependencies = append(dependencies, db)
	}
	web, err := New(nil, dependencies...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := web.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return web, tokens
}

func waitManagementResult(t *testing.T, web *Handler, id string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/management/jobs/"+id, nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		response := httptest.NewRecorder()
		web.Routes().ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatalf("poll: %d %s", response.Code, response.Body)
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.Status != "running" {
			return response.Body.String()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("management job did not finish")
	return ""
}

func dispatchManagement(t *testing.T, web *Handler, op, body string) string {
	t.Helper()
	response := managementRequest(web, op, body, "valid-token")
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch %s: %d %s", op, response.Code, response.Body)
	}
	var result struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.JobID == "" {
		t.Fatal("missing job ID")
	}
	return waitManagementResult(t, web, result.JobID)
}

func TestManagementAPIRequiresIndependentServerScope(t *testing.T) {
	web, tokens := managementTestHandler(t, apiApplications(), nil)
	for _, op := range application.ManagementOperations() {
		for _, test := range []struct {
			token  string
			scope  application.APIToken
			status int
		}{
			{"", application.APIToken{}, 401},
			{"valid-token", validAPIToken(), 403},
			{"valid-token", application.APIToken{Scope: application.APITokenScopeManage, ApplicationID: 7}, 403},
			{"invalid", application.APIToken{Scope: application.APITokenScopeManage}, 401},
		} {
			tokens.authToken = test.scope
			if response := managementRequest(web, op.Name, `{}`, test.token); response.Code != test.status {
				t.Fatalf("%s: %d want %d", op.Name, response.Code, test.status)
			}
		}
	}
	tokens.authToken = application.APIToken{ID: 5, Scope: application.APITokenScopeManage}
	tokens.authErr = application.ErrAPITokenExpired
	if response := managementRequest(web, "list_applications", `{}`, "valid-token"); response.Code != 401 {
		t.Fatal("expired token accepted")
	}
	tokens.authErr = nil
	// A management token cannot use the separate application-run API.
	req := httptest.NewRequest("POST", "/api/v1/applications/7/services/migrate/run", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("run API scope changed")
	}
}

func TestManagementAPIApplicationServiceEnvironmentAndRouting(t *testing.T) {
	apps := apiApplications()
	web, _ := managementTestHandler(t, apps, nil)
	cases := []struct {
		op, body string
		check    func() bool
	}{
		{"rename_application", `{"application_id":7,"name":"New name"}`, func() bool { return apps.renameID == 7 && apps.renameName == "New name" }},
		{"create_service", `{"application_id":7,"configuration":{"service_name":"web","image_name":"nginx:stable","auto_start":false}}`, func() bool {
			return apps.applicationContainerInput.ServiceName == "web" && apps.applicationContainerInput.ImageName == "nginx:stable"
		}},
		{"update_service", `{"application_id":7,"service_name":"web","configuration":{"service_name":"web","image_name":"nginx:latest"}}`, func() bool {
			return apps.applicationContainerUpdateName == "web" && apps.applicationContainerUpdateInput.ImageName == "nginx:latest"
		}},
		{"service_action", `{"application_id":7,"service_name":"migrate","action":"run"}`, func() bool { return apps.serviceActionID == 7 && apps.serviceAction == "run" }},
		{"add_variable", `{"application_id":7,"name":"SETTING","value":""}`, func() bool { return apps.variableAddID == 7 && apps.variableAddValue == "" }},
		{"update_variable", `{"application_id":7,"original_name":"OLD","name":"NEW","value":"replacement"}`, func() bool { return apps.variableOriginalName == "OLD" && apps.variableUpdateName == "NEW" }},
		{"delete_variable", `{"application_id":7,"name":"SETTING"}`, func() bool { return apps.variableDeleteName == "SETTING" }},
		{"add_secret", `{"application_id":7,"name":"PASSWORD","value":"synthetic-secret"}`, func() bool { return apps.secretAddValue == "synthetic-secret" }},
		{"update_secret", `{"application_id":7,"original_name":"PASSWORD","name":"PASSWORD","value":""}`, func() bool { return apps.secretUpdateValue == "" && apps.secretUpdateReplaceValue }},
		{"delete_secret", `{"application_id":7,"name":"PASSWORD"}`, func() bool { return apps.secretDeleteName == "PASSWORD" }},
		{"create_domain", `{"application_id":7,"name":"example.com"}`, func() bool { return apps.domainCreateID == 7 && apps.domainCreateName == "example.com" }},
		{"create_routing", `{"application_id":7,"domain_id":8,"routing":{"path":"/","service_name":"web","service_port":80}}`, func() bool { return apps.routingCreateDomainID == 8 && apps.routingCreateInput.ServicePort == 80 }},
		{"update_routing", `{"application_id":7,"domain_id":8,"routing_id":9,"routing":{"path":"/api","service_name":"web","service_port":8080}}`, func() bool { return apps.routingUpdateID == 9 && apps.routingUpdateInput.ServicePort == 8080 }},
		{"delete_routing", `{"application_id":7,"domain_id":8,"routing_id":9}`, func() bool { return apps.routingDeleteID == 9 }},
		{"delete_domain", `{"application_id":7,"name":"example.com"}`, func() bool { return apps.domainDeleteName == "example.com" }},
		{"delete_service", `{"application_id":7,"service_name":"web"}`, func() bool { return apps.serviceDeleteName == "web" }},
		{"delete_application", `{"application_id":7}`, func() bool { return apps.applicationDeleteID == 7 }},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			result := dispatchManagement(t, web, tc.op, tc.body)
			if !strings.Contains(result, `"complete"`) || !tc.check() {
				t.Fatalf("operation %s failed: %s", tc.op, result)
			}
			if strings.Contains(result, "synthetic-secret") {
				t.Fatal("secret echoed")
			}
		})
	}
	apps.environmentFiles = application.EnvironmentFiles{Variables: []application.EnvironmentVariable{{Key: "TOKEN", Value: "misplaced-secret"}}, Secrets: []application.EnvironmentVariable{{Key: "PASSWORD", Value: "synthetic-secret"}}}
	response := managementRequest(web, "list_environment", `{"application_id":7}`, "valid-token")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "PASSWORD") || strings.Contains(response.Body.String(), "synthetic-secret") || strings.Contains(response.Body.String(), "misplaced-secret") {
		t.Fatal("unsafe environment inventory", response.Body.String())
	}
}

func TestManagementAPIRunServiceWithParametersReturnsCapturedResult(t *testing.T) {
	apps := apiApplications()
	apps.serviceActionResult = application.OneOffRunResult{
		ExitCode:  0,
		Stdout:    "migration complete\n",
		Stderr:    "",
		Truncated: false,
	}
	web, _ := managementTestHandler(t, apps, nil)
	result := dispatchManagement(t, web, "service_action", `{"application_id":7,"service_name":"migrate","action":"run","parameters":{"args":["--database-name","example","--confirm"],"env":{"NODE_ENV":"development"}}}`)
	for _, expected := range []string{`"complete"`, `"exit_code":0`, `"stdout":"migration complete\n"`} {
		if !strings.Contains(result, expected) {
			t.Fatalf("result did not contain %q: %s", expected, result)
		}
	}
	if got := strings.Join(apps.serviceActionParameters.Args, "\x00"); got != "--database-name\x00example\x00--confirm" {
		t.Fatalf("arguments = %q, want ordered values", got)
	}
	if apps.serviceActionParameters.Env["NODE_ENV"] != "development" {
		t.Fatalf("environment = %#v, want NODE_ENV=development", apps.serviceActionParameters.Env)
	}
}

func TestManagementAPIRunFailureReturnsCapturedResult(t *testing.T) {
	apps := apiApplications()
	apps.serviceActionErr = errors.New("run service: migration failed")
	apps.serviceActionResult = application.OneOffRunResult{ExitCode: 2, Stdout: "partial\n", Stderr: "failure\n"}
	web, _ := managementTestHandler(t, apps, nil)
	result := dispatchManagement(t, web, "service_action", `{"application_id":7,"service_name":"migrate","action":"run","parameters":{"args":["--confirm"]}}`)
	for _, expected := range []string{`"failed"`, `"exit_code":2`, `"stdout":"partial\n"`, `"stderr":"failure\n"`} {
		if !strings.Contains(result, expected) {
			t.Fatalf("failed result did not contain %q: %s", expected, result)
		}
	}
}

func TestManagementAPIDatabasesAndSafeFailures(t *testing.T) {
	db := &fakeManagedDatabases{credentials: &application.ManagedDatabaseCredentials{Username: "owner", Password: "generated-secret"}}
	web, _ := managementTestHandler(t, apiApplications(), db)
	cases := []struct {
		op, body string
		check    func() bool
	}{
		{"enable_database_cluster", `{"version":"17","default_user":"redlaunch","password":"synthetic-password"}`, func() bool { return db.enableInput.Password == "synthetic-password" }},
		{"create_database_user", `{"username":"reporter","password":"synthetic-password","databases":[]}`, func() bool { return db.createdUser == "reporter" }},
		{"create_database", `{"name":"analytics","owner":"reporter"}`, func() bool { return db.createdDB == "analytics" && db.createdOwner == "reporter" }},
		{"update_database_user_password", `{"username":"reporter","password":"rotated-synthetic"}`, func() bool { return db.passwordUser == "reporter" }},
		{"set_database_user_permissions", `{"username":"reporter","databases":["analytics"]}`, func() bool { return db.permsUser == "reporter" && len(db.grants) == 1 }},
		{"delete_database", `{"name":"analytics"}`, func() bool { return db.droppedDB == "analytics" }},
		{"delete_database_user", `{"username":"reporter"}`, func() bool { return db.deletedUser == "reporter" }},
		{"disable_database_cluster", `{}`, func() bool { return !db.cluster.Enabled }},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			result := dispatchManagement(t, web, tc.op, tc.body)
			if !strings.Contains(result, `"complete"`) || !tc.check() {
				t.Fatal("operation failed", result)
			}
			if strings.Contains(result, "secret") || strings.Contains(result, "synthetic-password") {
				t.Fatal("credentials echoed", result)
			}
		})
	}
	db.userErr = errors.New("synthetic-password must never be disclosed")
	result := dispatchManagement(t, web, "update_database_user_password", `{"username":"reporter","password":"synthetic-password"}`)
	if !strings.Contains(result, `"failed"`) || strings.Contains(result, "synthetic-password") {
		t.Fatal("unsafe failure", result)
	}
}

func TestManagementAPIProxySettings(t *testing.T) {
	cache := "private, no-store"
	fake := &fakeProxyPolicyService{
		fakeApplicationService: fakeApplicationService{applications: []application.Application{{ID: 7, Name: "Proxy app"}}},
		global:                 application.ProxySettings{SecurityHeaders: &application.ProxySecurityHeaders{ContentTypeOptions: "nosniff", FrameOptions: "DENY"}},
		policy:                 application.ProxySettings{CacheControl: &cache},
	}
	tokens := newFakeAPITokenService()
	tokens.authToken = application.APIToken{ID: 5, Scope: application.APITokenScopeManage}
	web, err := New(nil, fake, tokens)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := web.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	response := managementRequest(web, "get_proxy_settings", `{}`, "valid-token")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content_type_options":"nosniff"`) || !strings.Contains(response.Body.String(), `"effective"`) {
		t.Fatalf("global proxy settings = %d %s", response.Code, response.Body)
	}
	response = managementRequest(web, "get_application_proxy_settings", `{"application_id":7}`, "valid-token")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"application_id":7`) || !strings.Contains(response.Body.String(), `"cache_control":"private, no-store"`) || !strings.Contains(response.Body.String(), `"frame_options":"DENY"`) {
		t.Fatalf("application proxy settings = %d %s", response.Code, response.Body)
	}
	response = managementRequest(web, "update_proxy_settings", `{"settings":{"security_headers":{"frame_options":"ALLOWALL"}}}`, "valid-token")
	if response.Code != http.StatusBadRequest || fake.saved != 0 {
		t.Fatalf("invalid proxy settings = %d, saves %d", response.Code, fake.saved)
	}

	result := dispatchManagement(t, web, "update_proxy_settings", `{"settings":{"security_headers":{"content_type_options":"nosniff"}}}`)
	if !strings.Contains(result, `"complete"`) || fake.saved != 1 || fake.savedID != 0 || fake.policy.SecurityHeaders == nil || fake.policy.SecurityHeaders.ContentTypeOptions != "nosniff" || fake.policy.CacheControl != nil {
		t.Fatalf("global update = %s, %#v", result, fake)
	}
	result = dispatchManagement(t, web, "update_application_proxy_settings", `{"application_id":7,"settings":{"security_headers":{}}}`)
	if !strings.Contains(result, `"complete"`) || fake.saved != 2 || fake.savedID != 7 || fake.policy.SecurityHeaders == nil || fake.policy.SecurityHeaders.ContentTypeOptions != "" || fake.policy.CacheControl != nil {
		t.Fatalf("application update = %s, %#v", result, fake)
	}
	response = managementRequest(web, "get_application_proxy_settings", `{"application_id":7}`, "valid-token")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"security_headers":{"content_type_options":""`) {
		t.Fatalf("disabled proxy category = %d %s", response.Code, response.Body)
	}
}

func TestManagementAPIRejectsMalformedRequestsBeforeDispatch(t *testing.T) {
	apps := apiApplications()
	web, _ := managementTestHandler(t, apps, nil)
	for _, body := range []string{`null`, `{"application_id":7,"name":"test","value":"unexpected"}`, `{"name":"test"}`, `{"application_id":-1,"name":"test"}`, `{"application_id":7,"name":null}`, strings.Repeat("x", (1<<20)+1)} {
		if response := managementRequest(web, "rename_application", body, "valid-token"); response.Code != 400 {
			t.Fatal("malformed request accepted", response.Code)
		}
	}
	if apps.renameID != 0 {
		t.Fatal("invalid request triggered operation")
	}
	response := managementRequest(web, "arbitrary_command", `{}`, "valid-token")
	if response.Code != 404 {
		t.Fatal("unknown operation accepted")
	}
}

func TestManagementJobsEnforceAuthenticationExpiryAndCapacity(t *testing.T) {
	web, tokens := managementTestHandler(t, apiApplications(), nil)
	web.managementJobs.jobs["expired"] = &managementJob{status: "complete", finishedAt: time.Now().Add(-2 * time.Hour)}
	request := httptest.NewRequest("GET", "/api/v1/management/jobs/expired", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatal("expired job accessible")
	}
	tokens.authToken = validAPIToken()
	response = httptest.NewRecorder()
	web.Routes().ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("run token accessed management jobs")
	}
	tokens.authToken = application.APIToken{Scope: application.APITokenScopeManage}
	for i := 0; i < 1024; i++ {
		web.managementJobs.jobs[time.Unix(int64(i), 0).String()] = &managementJob{status: "complete", finishedAt: time.Now()}
	}
	if response := managementRequest(web, "delete_application", `{"application_id":7}`, "valid-token"); response.Code != 503 {
		t.Fatal("unbounded job retention")
	}
}

func managementVersionHandler(t *testing.T, versions *fakeVersionService) (*Handler, *fakeAPITokenService) {
	t.Helper()
	apps := &fakeApplicationService{
		applications: []application.Application{{ID: 7, Name: "Acme", FolderName: "acme"}},
		services:     []application.Service{{ID: 1, ApplicationID: 7, Name: "web"}},
	}
	tokens := newFakeAPITokenService()
	tokens.authToken = application.APIToken{ID: 5, Scope: application.APITokenScopeManage}
	web, err := New(nil, apps, versions, tokens)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := web.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return web, tokens
}

func TestManagementListServiceVersions(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := managementVersionHandler(t, versions)

	response := managementRequest(web, "list_service_versions", `{"application_id":7,"service_name":"web"}`, "valid-token")
	if response.Code != http.StatusOK {
		t.Fatalf("list_service_versions: %d %s", response.Code, response.Body)
	}
	body := response.Body.String()
	for _, expected := range []string{"acme-app", "development", "RegistryAvailable"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("list_service_versions result is missing %q: %s", expected, body)
		}
	}
}

func TestManagementSwitchVersionByDigest(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := managementVersionHandler(t, versions)

	digest := "sha256:" + strings.Repeat("a", 64)
	result := dispatchManagement(t, web, "switch_version", `{"application_id":7,"service_name":"web","digest":"`+digest+`"}`)
	if !strings.Contains(result, "updated") {
		t.Fatalf("switch_version result = %s, want an update confirmation", result)
	}
	waitForVersionDeploy(t, versions, digest, versions.deployed)
}

func TestManagementSwitchVersionByTag(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := managementVersionHandler(t, versions)

	dispatchManagement(t, web, "switch_version", `{"application_id":7,"service_name":"web","tag":"build-1"}`)
	waitForVersionDeploy(t, versions, "sha256:"+strings.Repeat("a", 64), versions.deployed)
}

func TestManagementSwitchVersionRejectsUnknownTarget(t *testing.T) {
	versions := &fakeVersionService{versions: versionTestVersions()}
	web, _ := managementVersionHandler(t, versions)

	body := dispatchManagement(t, web, "switch_version", `{"application_id":7,"service_name":"web","tag":"missing"}`)
	if !strings.Contains(body, "failed") {
		t.Fatalf("switch_version result = %s, want a failed job for an unknown tag", body)
	}
	body = dispatchManagement(t, web, "switch_version", `{"application_id":7,"service_name":"web"}`)
	if !strings.Contains(body, "failed") {
		t.Fatalf("switch_version result = %s, want a failed job without a target", body)
	}
	if versions.deployed() != "" {
		t.Fatal("rejected switch_version calls must not deploy a version")
	}
}

func TestManagementSwitchApplicationVersion(t *testing.T) {
	versions := &fakeVersionService{bulkOutcomes: []application.VersionDeployOutcome{
		{ServiceName: "web", State: application.VersionDeployStateDeployed, Detail: "The deployment tag now points at build-1."},
		{ServiceName: "worker", State: application.VersionDeployStateSkipped, Detail: "The repository acme-worker has no tag build-1."},
	}}
	web, _ := managementVersionHandler(t, versions)

	body := dispatchManagement(t, web, "switch_application_version", `{"application_id":7,"tag":"build-1"}`)
	if !strings.Contains(body, "deployed") || !strings.Contains(body, "skipped") {
		t.Fatalf("switch_application_version result = %s, want per-service outcomes", body)
	}
	waitForVersionDeploy(t, versions, "build-1", versions.bulkDeployed)
}
