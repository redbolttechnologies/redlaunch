package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type remoteTestTransport struct{ calls int }

func (t *remoteTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	recorder.WriteString(`{"applications":[]}`)
	return recorder.Result(), nil
}

func remoteMessage(s *Server, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp/manage", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", protocolVersion)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestRemoteMCPStatelessMessages(t *testing.T) {
	transport := &remoteTestTransport{}
	s, err := NewRemote("credential", true, transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":"tools","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_applications","arguments":{}}}`,
	} {
		w := remoteMessage(s, body)
		var result map[string]json.RawMessage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["result"] == nil {
			t.Fatalf("remote response = %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Mcp-Session-Id") != "" {
			t.Fatal("stateless transport issued a session ID")
		}
	}
	if transport.calls != 1 {
		t.Fatalf("API calls = %d, want 1", transport.calls)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"list_applications","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":99,"result":{}}`,
	} {
		w := remoteMessage(s, body)
		if w.Code != 202 || w.Body.Len() != 0 {
			t.Fatalf("notification = %d %s", w.Code, w.Body.String())
		}
	}
	if transport.calls != 1 {
		t.Fatal("notification executed a tool")
	}
}

func TestRemoteMCPTransportValidation(t *testing.T) {
	s, _ := NewRemote("credential", false, &remoteTestTransport{})
	for _, tc := range []struct {
		name, method, content, accept, version, body string
		status                                       int
	}{
		{"GET", "GET", "application/json", "application/json, text/event-stream", protocolVersion, "", 405},
		{"DELETE", "DELETE", "application/json", "application/json, text/event-stream", protocolVersion, "", 405},
		{"content type", "POST", "text/plain", "application/json, text/event-stream", protocolVersion, "{}", 415},
		{"accept", "POST", "application/json", "application/json", protocolVersion, "{}", 406},
		{"version", "POST", "application/json", "application/json, text/event-stream", "wrong", "{}", 400},
		{"oversize", "POST", "application/json", "application/json, text/event-stream", protocolVersion, strings.Repeat("x", maxMessageBytes+1), 413},
		{"default version", "POST", "application/json", "application/json, text/event-stream", "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/mcp/apps/1", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.content)
			r.Header.Set("Accept", tc.accept)
			r.Header.Set("MCP-Protocol-Version", tc.version)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
	for _, body := range []string{`[{}]`, `{`, `{"jsonrpc":"2.0","id":{},"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`} {
		w := remoteMessage(s, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("invalid message accepted: %s", w.Body.String())
		}
	}
}
