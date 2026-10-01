package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

const handshake = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"

type testResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func session(t *testing.T, server *Server, input string) []testResponse {
	t.Helper()
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var responses []testResponse
	for {
		var response testResponse
		if err := decoder.Decode(&response); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("stdout contains a non-protocol message")
		}
		responses = append(responses, response)
	}
	return responses
}

func TestOfflineDiscoveryAndBundledGuides(t *testing.T) {
	server, err := New("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	input := handshake + `{"jsonrpc":"2.0","id":"tools","method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"resources/list"}` + "\n"
	for i, g := range guides {
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 4, "method": "resources/read", "params": map[string]string{"uri": g.URI}})
		input += string(line) + "\n"
	}
	responses := session(t, server, input)
	if len(responses) != len(guides)+3 {
		t.Fatalf("response count = %d", len(responses))
	}
	var list struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(responses[1].Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 2 {
		t.Fatalf("offline tools count = %d", len(list.Tools))
	}
	if string(responses[1].ID) != `"tools"` {
		t.Fatal("string request ID lost")
	}
	for i, response := range responses[3:] {
		var result struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}
		if response.Error != nil || json.Unmarshal(response.Result, &result) != nil || len(result.Contents) != 1 || !strings.HasPrefix(result.Contents[0].Text, "# ") {
			t.Fatalf("bundled guide %s unavailable", guides[i].Name)
		}
	}
}

func TestProtocolValidationAndLifecycle(t *testing.T) {
	cases := []struct {
		name, input string
		code        int
	}{
		{"parse", "{", -32700},
		{"batch", `[]`, -32600},
		{"version", `{"id":1,"method":"ping"}`, -32600},
		{"null ID", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, -32600},
		{"float ID", `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, -32600},
		{"before init", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, -32000},
		{"bad initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, -32602},
		{"unknown method", handshake + `{"jsonrpc":"2.0","id":2,"method":"unknown"}`, -32601},
		{"unknown resource", handshake + `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"file:///etc/passwd"}}`, -32002},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := New("", "", false)
			responses := session(t, server, tc.input+"\n")
			response := responses[len(responses)-1]
			if response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("error = %#v, want %d", response.Error, tc.code)
			}
		})
	}
	server, _ := New("", "", false)
	responses := session(t, server, handshake+`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`+"\n"+`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"run_service"}}`+"\n")
	if len(responses) != 1 {
		t.Fatal("notifications received a response")
	}
}

func TestVersionNegotiation(t *testing.T) {
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18", protocolVersion, "future"} {
		server, _ := New("", "", false)
		responses := session(t, server, strings.Replace(handshake, protocolVersion, version, 1))
		want := version
		if version == "future" {
			want = protocolVersion
		}
		var result struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(responses[0].Result, &result) != nil || result.ProtocolVersion != want {
			t.Fatalf("negotiation failed for %s", version)
		}
	}
}

func TestToolValidation(t *testing.T) {
	server, _ := New("", "", false)
	cases := []struct{ name, args string }{
		{"run_service", `{"application_id":7,"service_name":"migrate"}`},
		{"get_run_status", `{"application_id":7,"service_name":"migrate","job_id":"job"}`},
		{"generate_run_workflow", `{"application_id":7,"service_name":"../migrate"}`},
		{"generate_run_workflow", `{"application_id":0,"service_name":"migrate"}`},
		{"generate_run_workflow", `{"application_id":7.5,"service_name":"migrate"}`},
		{"generate_run_workflow", `{"application_id":7,"service_name":"$(id)"}`},
		{"generate_run_workflow", `{"application_id":7,"service_name":"migrate","token":"test"}`},
		{"generate_run_workflow", `{"application_id":7,"service_name":"migrate","job_id":""}`},
		{"read_guide", `{"topic":"../secrets.env"}`},
		{"read_guide", `null`},
	}
	for _, tc := range cases {
		_, err := server.call(context.Background(), tc.name, json.RawMessage(tc.args))
		if err == nil || err.Code != -32602 {
			t.Fatalf("tool %s accepted invalid input", tc.name)
		}
	}
	result, err := server.call(context.Background(), "generate_run_workflow", json.RawMessage(`{"application_id":7,"service_name":"migrate"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !bytes.Contains(encoded, []byte("workflow_dispatch")) {
		t.Fatal("workflow missing")
	}
}

func TestBoundedInputAndCancellation(t *testing.T) {
	server, _ := New("", "", false)
	if err := server.Serve(context.Background(), strings.NewReader(strings.Repeat("a", maxMessageBytes+1)), io.Discard); err == nil {
		t.Fatal("oversized input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx, strings.NewReader(handshake), io.Discard); err != nil {
		t.Fatal(err)
	}
}
