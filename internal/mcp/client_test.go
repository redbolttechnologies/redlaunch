package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Synthetic credentials only. Tests never read a personal environment file.
func testToken() string { return "rlr_" + strings.Repeat("a", 64) }

func TestAPIConfigurationValidation(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost:8080", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=secret", "https://example.com#secret", "file:///etc/passwd", "//example.com", "http://127.0.0.1.example.com"} {
		if _, err := New(origin, testToken(), false); err == nil {
			t.Fatalf("accepted unsafe origin %q", origin)
		}
	}
	for _, origin := range []string{"https://example.com", "http://127.0.0.1:8080", "http://[::1]:8080/"} {
		if _, err := New(origin, testToken(), false); err != nil {
			t.Fatalf("rejected safe origin: %v", err)
		}
	}
	for _, input := range [][2]string{{"", "invalid"}, {"https://example.com", ""}, {"https://example.com", "invalid\r\nheader"}} {
		if _, err := New(input[0], input[1], false); err == nil {
			t.Fatal("accepted incomplete credentials")
		}
	}
	if _, err := New("", "", true); err == nil {
		t.Fatal("allow-run accepted without credentials")
	}
}

func TestLiveDispatchAndPoll(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+testToken() {
			t.Error("missing bearer authorization")
		}
		if r.URL.Path == "/api/v1/applications/7/services/migrate/run" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"status":"running","job_id":"job-1","status_url":"https://untrusted.example/"}`)
		} else if r.URL.Path == "/api/v1/applications/7/services/migrate/run/status" && r.Method == http.MethodGet && r.URL.Query().Get("id") == "job-1" {
			fmt.Fprintf(w, `{"status":"failed","detail":%q}`, testToken())
		} else {
			t.Error("unexpected API request")
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	server, err := New(upstream.URL, testToken(), true)
	if err != nil {
		t.Fatal(err)
	}
	input := handshake + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_service","arguments":{"application_id":7,"service_name":"migrate"}}}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_run_status","arguments":{"application_id":7,"service_name":"migrate","job_id":"job-1"}}}` + "\n"
	responses := session(t, server, input)
	if calls != 2 || len(responses) != 3 {
		t.Fatalf("calls=%d responses=%d", calls, len(responses))
	}
	for _, r := range responses[1:] {
		if r.Error != nil {
			t.Fatal(r.Error)
		}
		if strings.Contains(string(r.Result), testToken()) || strings.Contains(string(r.Result), "untrusted.example") {
			t.Fatal("sensitive upstream content forwarded")
		}
	}
	if !strings.Contains(string(responses[1].Result), "job-1") || !strings.Contains(string(responses[2].Result), "failed") {
		t.Fatal("dispatch or polling result missing")
	}
	readOnly, _ := New(upstream.URL, testToken(), false)
	if len(readOnly.tools()) != 3 || len(server.tools()) != 4 {
		t.Fatal("incorrect live tool capabilities")
	}
	if _, err := readOnly.call(context.Background(), "run_service", json.RawMessage(`{"application_id":7,"service_name":"migrate"}`)); err == nil {
		t.Fatal("dispatch possible without --allow-run")
	}
	if calls != 2 {
		t.Fatal("disabled tool contacted API")
	}
}

func TestRedirectAndUpstreamErrorsDoNotDiscloseSecrets(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	for _, code := range []int{302, 401, 403, 404, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(code)
				fmt.Fprint(w, testToken())
			}))
			defer upstream.Close()
			server, err := New(upstream.URL, testToken(), true)
			if err != nil {
				t.Fatal(err)
			}
			result, rpcErr := server.call(context.Background(), "run_service", json.RawMessage(`{"application_id":7,"service_name":"migrate"}`))
			if rpcErr != nil {
				t.Fatal("API error reported as protocol error")
			}
			encoded, _ := json.Marshal(result)
			if !strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), fmt.Sprint(code)) || strings.Contains(string(encoded), testToken()) {
				t.Fatal("unsafe or missing API error result")
			}
		})
	}
	if redirected {
		t.Fatal("redirect followed")
	}
}

func TestInvalidUpstreamResponses(t *testing.T) {
	for _, body := range []string{`not JSON`, `{"status":"other","job_id":"job"}`, `{"status":"running"}`, `{"status":"running","job_id":"../escape"}`, `{"status":"running","job_id":"` + testToken() + `"}`, strings.Repeat("x", 65537)} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); fmt.Fprint(w, body) }))
		client, err := newAPIClient(upstream.URL, testToken())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.run(context.Background(), 7, "migrate"); err == nil {
			t.Fatal("invalid upstream response accepted")
		}
		upstream.Close()
	}
}

func TestLiveInputValidationPrecedesHTTP(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer upstream.Close()
	server, _ := New(upstream.URL, testToken(), true)
	for _, raw := range []string{`{"application_id":7,"service_name":"../migrate","job_id":"job"}`, `{"application_id":7,"service_name":"migrate","job_id":"../../other?token=secret"}`, `{"application_id":-1,"service_name":"migrate","job_id":"job"}`, `{"application_id":7,"service_name":"migrate","job_id":"job","url":"https://other.example"}`} {
		if _, err := server.call(context.Background(), "get_run_status", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid API arguments accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached API")
	}
}
