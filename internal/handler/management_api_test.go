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
