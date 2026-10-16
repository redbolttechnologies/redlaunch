package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"redlaunch/internal/application"
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
