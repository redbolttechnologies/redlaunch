package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/handler"
	"redlaunch/internal/mcp"
	"redlaunch/internal/service"
	"redlaunch/internal/store"
)

// Exercise the public stdio interface rather than private tool dispatch. This
// integration test lives outside package mcp because handlers also serve MCP.
func managementCall(t *testing.T, s *mcp.Server, name, raw string) json.RawMessage {
	t.Helper()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, raw) + "\n"
	var output bytes.Buffer
	if err := s.Serve(t.Context(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil || len(response.Error) > 0 {
		t.Fatalf("MCP call failed: %v %s", err, response.Error)
	}
	return response.Result
}

func managementToolResult(t *testing.T, s *mcp.Server, name, raw string) map[string]json.RawMessage {
	t.Helper()
	encoded := managementCall(t, s, name, raw)
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

func waitManagementTool(t *testing.T, s *mcp.Server, result map[string]json.RawMessage) map[string]json.RawMessage {
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
	s, err := mcp.NewWithManagement(upstream.URL, "", credential.Plaintext, false, true)
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
	run, err := tokens.Create(t.Context(), application.APITokenInput{DisplayName: "Run", ApplicationID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	scoped, _ := mcp.NewWithManagement(upstream.URL, "", run.Plaintext, false, true)
	encoded := managementCall(t, scoped, "list_applications", `{}`)
	if !strings.Contains(string(encoded), "403") {
		t.Fatal("run token granted management")
	}
	if err := tokens.Revoke(t.Context(), credential.Token.ID); err != nil {
		t.Fatal(err)
	}
	encoded = managementCall(t, s, "list_applications", `{}`)
	if !strings.Contains(string(encoded), "401") || strings.Contains(string(encoded), credential.Plaintext) {
		t.Fatal("revoked management token accepted or disclosed")
	}
}
