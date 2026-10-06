package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/handler"
	"redlaunch/internal/service"
	"redlaunch/internal/store"
)

func TestManagementToolsRequireSeparateCredentialAndMutationOptIn(t *testing.T) {
	for _, tc := range []struct {
		run, manage           string
		allowRun, allowManage bool
		wantError             bool
	}{
		{"", "", false, true, true},
		{testToken(), "", true, true, true},
		{"", testToken(), true, false, true},
		{"", testToken(), false, false, false},
		{testToken(), testToken(), true, true, false},
	} {
		s, err := NewWithManagement("https://example.com", tc.run, tc.manage, tc.allowRun, tc.allowManage)
		if (err != nil) != tc.wantError {
			t.Fatalf("configuration: %v", err)
		}
		if err != nil {
			continue
		}
		for _, op := range application.ManagementOperations() {
			enabled := false
			for _, tool := range s.tools() {
				if tool.Name == op.Name {
					enabled = true
				}
			}
			if enabled != (op.ReadOnly || tc.allowManage) {
				t.Fatalf("wrong tool capability for %s", op.Name)
			}
		}
		if !tc.allowManage {
			if _, err := s.call(t.Context(), "delete_application", json.RawMessage(`{"application_id":1}`)); err == nil {
				t.Fatal("disabled mutation callable")
			}
		}
	}
}

func managementToolResult(t *testing.T, s *Server, name, raw string) map[string]json.RawMessage {
	t.Helper()
	response, rpcErr := s.call(t.Context(), name, json.RawMessage(raw))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	encoded, _ := json.Marshal(response)
	var tool struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(encoded, &tool); err != nil || len(tool.Content) != 1 || tool.IsError {
		t.Fatalf("tool failure: %s", encoded)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tool.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func waitManagementTool(t *testing.T, s *Server, result map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var id string
	if err := json.Unmarshal(result["job_id"], &id); err != nil || id == "" {
		t.Fatal("missing job ID")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result = managementToolResult(t, s, "get_management_job", fmt.Sprintf(`{"job_id":%q}`, id))
		if string(result["status"]) == `"complete"` {
			return result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("management job timed out")
	return nil
}

func TestManagementMCPThroughRealHTTPServices(t *testing.T) {
	database, err := store.Open(t.Context(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	apps, err := service.NewApplications(database, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := service.NewAPITokenService(database, apps)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := tokens.Create(t.Context(), application.APITokenInput{DisplayName: "Test management", Scope: application.APITokenScopeManage})
	if err != nil {
		t.Fatal(err)
	}
	web, err := handler.NewForTests(nil, apps, tokens)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Shutdown(context.Background())
	upstream := httptest.NewServer(web.Routes())
	defer upstream.Close()
	s, err := NewWithManagement(upstream.URL, "", credential.Plaintext, false, true)
	if err != nil {
		t.Fatal(err)
	}
	created := waitManagementTool(t, s, managementToolResult(t, s, "create_application", `{"name":"Test app","folder_name":"test-app"}`))
	var app application.Application
	if err := json.Unmarshal(created["result"], &app); err != nil || app.ID < 1 {
		t.Fatal("missing application metadata", err)
	}
	inventory := managementToolResult(t, s, "list_applications", `{}`)
	if !strings.Contains(string(inventory["result"]), "Test app") {
		t.Fatal("application not listed")
	}
	for _, kind := range []string{"variable", "secret"} {
		waitManagementTool(t, s, managementToolResult(t, s, "add_"+kind, fmt.Sprintf(`{"application_id":%d,"name":"TEST_%s","value":"synthetic-value"}`, app.ID, strings.ToUpper(kind))))
		waitManagementTool(t, s, managementToolResult(t, s, "update_"+kind, fmt.Sprintf(`{"application_id":%d,"original_name":"TEST_%s","name":"RENAMED_%s","value":""}`, app.ID, strings.ToUpper(kind), strings.ToUpper(kind))))
	}
	inventory = managementToolResult(t, s, "list_environment", fmt.Sprintf(`{"application_id":%d}`, app.ID))
	content := string(inventory["result"])
	if strings.Contains(content, "synthetic-value") || !strings.Contains(content, "RENAMED_SECRET") {
		t.Fatal("unsafe environment inventory", content)
	}
	files, err := apps.GetEnvironmentFiles(t.Context(), app.ID)
	if err != nil || len(files.Variables) != 1 || len(files.Secrets) != 1 || files.Secrets[0].Value != "" {
		t.Fatal("environment mutation did not preserve empty values", err)
	}
	for _, kind := range []string{"variable", "secret"} {
		waitManagementTool(t, s, managementToolResult(t, s, "delete_"+kind, fmt.Sprintf(`{"application_id":%d,"name":"RENAMED_%s"}`, app.ID, strings.ToUpper(kind))))
	}
	// A run credential is rejected by the server even if the operator mistakenly
	// places it in the management environment variable.
	run, err := tokens.Create(t.Context(), application.APITokenInput{DisplayName: "Run", ApplicationID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	scoped, _ := NewWithManagement(upstream.URL, "", run.Plaintext, false, true)
	result, rpcErr := scoped.call(t.Context(), "list_applications", json.RawMessage(`{}`))
	encoded, _ := json.Marshal(result)
	if rpcErr != nil || !strings.Contains(string(encoded), "403") {
		t.Fatal("run token granted management")
	}
	if err := tokens.Revoke(t.Context(), credential.Token.ID); err != nil {
		t.Fatal(err)
	}
	result, rpcErr = s.call(t.Context(), "list_applications", json.RawMessage(`{}`))
	encoded, _ = json.Marshal(result)
	if rpcErr != nil || !strings.Contains(string(encoded), "401") || strings.Contains(string(encoded), credential.Plaintext) {
		t.Fatal("revoked management token accepted or disclosed")
	}
}

func TestManagementTransportRejectsSensitiveErrorsAndRedirects(t *testing.T) {
	var contacted bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted = true }))
	defer target.Close()
	for _, code := range []int{302, 400, 401, 403, 500} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(code)
			fmt.Fprint(w, testToken())
		}))
		s, err := NewWithManagement(upstream.URL, "", testToken(), false, true)
		if err != nil {
			t.Fatal(err)
		}
		result, rpcErr := s.call(t.Context(), "list_applications", json.RawMessage(`{}`))
		encoded, _ := json.Marshal(result)
		if rpcErr != nil || strings.Contains(string(encoded), testToken()) || !strings.Contains(string(encoded), `"isError":true`) {
			t.Fatal("unsafe transport error")
		}
		upstream.Close()
	}
	if contacted {
		t.Fatal("management request followed redirect")
	}
}
