// Package mcp provides a local stdio MCP interface to public integration guides
// and the authenticated Redlaunch machine APIs. It never opens the
// database, reads local configuration files, or invokes Docker.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	documentation "redlaunch"
)

const protocolVersion = "2025-11-25"
const maxMessageBytes = 1 << 20

var integerID = regexp.MustCompile(`^-?[0-9]+$`)

// Server serves one MCP session. Each subprocess owns its own session.
type Server struct {
	client      *apiClient
	allowRun    bool
	management  *apiClient
	allowManage bool
}

// New allows credential-free documentation use. Supplying API configuration
// enables status polling; mutating runs require the explicit allowRun option.
func New(baseURL, token string, allowRun bool) (*Server, error) {
	s := &Server{allowRun: allowRun}
	if baseURL != "" || token != "" {
		client, err := newAPIClient(baseURL, token)
		if err != nil {
			return nil, err
		}
		s.client = client
	}
	if allowRun && s.client == nil {
		return nil, errors.New("--allow-run requires REDLAUNCH_URL and REDLAUNCH_RUN_TOKEN")
	}
	return s, nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func failure(code int, message string) *rpcError { return &rpcError{Code: code, Message: message} }

// Serve uses newline-delimited JSON-RPC. stdout contains protocol messages
// only. API operations are sequential and bounded by a 30 second timeout.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	encoder := json.NewEncoder(output)
	initialized, ready := false, false
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Bytes()
		var req request
		var result any
		var rpcErr *rpcError
		id := json.RawMessage("null")
		if !json.Valid(line) {
			rpcErr = failure(-32700, "Parse error")
		} else if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" {
			rpcErr = failure(-32600, "Invalid request")
		} else if req.Method == "" && len(req.ID) > 0 && (len(req.Result) > 0 || len(req.Error) > 0) {
			// No requests are sent to the client, so unsolicited responses are ignored.
			continue
		} else if req.Method == "" || (len(req.ID) > 0 && !validID(req.ID)) ||
			(len(req.Params) > 0 && req.Params[0] != '{') {
			rpcErr = failure(-32600, "Invalid request")
		} else if len(req.ID) == 0 {
			if req.Method == "notifications/initialized" && initialized {
				ready = true
			}
			// Notifications must never trigger tool side effects or receive a response.
			continue
		} else {
			id = req.ID
			switch req.Method {
			case "initialize":
				var params struct {
					ProtocolVersion string         `json:"protocolVersion"`
					Capabilities    map[string]any `json:"capabilities"`
					ClientInfo      struct {
						Name    string `json:"name"`
						Version string `json:"version"`
					} `json:"clientInfo"`
				}
				if initialized || json.Unmarshal(req.Params, &params) != nil || params.ProtocolVersion == "" ||
					params.Capabilities == nil || params.ClientInfo.Name == "" || params.ClientInfo.Version == "" {
					rpcErr = failure(-32602, "Invalid initialization parameters")
					break
				}
				version := protocolVersion
				switch params.ProtocolVersion {
				case "2024-11-05", "2025-03-26", "2025-06-18", protocolVersion:
					version = params.ProtocolVersion
				}
				result = map[string]any{
					"protocolVersion": version,
					"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
					"serverInfo":      map[string]string{"name": "redlaunch", "version": "1.0.0"},
					"instructions":    "Read the integration guides before creating workflows. Application-scoped run tokens run registered services and poll jobs. Management tools use a separate server-wide token; mutations require --allow-manage. Never echo secret values or embed credentials in workflows. Poll get_management_job after a management mutation; inspect failed or lost jobs before retrying.",
				}
				initialized = true
			case "ping":
				result = map[string]any{}
			default:
				if !ready {
					rpcErr = failure(-32000, "Initialize the MCP session first")
				} else {
					result, rpcErr = s.handle(ctx, req)
				}
			}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": id}
		if rpcErr != nil {
			response["error"] = rpcErr
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return errors.New("write MCP response failed")
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if scanner.Err() != nil {
		return errors.New("read MCP message failed (maximum size is 1 MiB)")
	}
	return nil
}

func validID(id json.RawMessage) bool {
	var value string
	return json.Unmarshal(id, &value) == nil && len(id) > 0 && id[0] == '"' || integerID.Match(id)
}

type guide struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
	file        string
}

var guides = []guide{
	{"redlaunch://guides/api-tokens", "api-tokens", "API scope, authentication, async runs, status codes, and rotation.", "text/markdown", "API_TOKENS.md"},
	{"redlaunch://guides/recipes", "recipes", "GitHub Actions workflows: publish images, migrate, deploy via registry or direct image copy; managed Compose identity.", "text/markdown", "RECIPES.md"},
	{"redlaunch://guides/ssh-keys", "ssh-keys", "SSH transport, permissions, known hosts, and key rotation.", "text/markdown", "SSH_KEYS.md"},
	{"redlaunch://guides/installation", "installation", "VPS installation and management endpoint reachability.", "text/markdown", "INSTALL.md"},
	{"redlaunch://guides/reference", "reference", "Configuration and managed resource layout.", "text/markdown", "README.md"},
	{"redlaunch://guides/mcp", "mcp", "MCP configuration and tool usage.", "text/markdown", "MCP.md"},
}

func readGuide(name string) (string, bool) {
	for _, g := range guides {
		if name == g.Name || name == g.URI {
			content, err := documentation.Files.ReadFile(g.file)
			return string(content), err == nil
		}
	}
	return "", false
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "resources/list":
		return map[string]any{"resources": guides}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.URI == "" {
			return nil, failure(-32602, "A resource URI is required")
		}
		for _, g := range guides {
			if g.URI == params.URI {
				content, ok := readGuide(g.URI)
				if !ok {
					return nil, failure(-32603, "Bundled guide could not be read")
				}
				return map[string]any{"contents": []any{map[string]string{"uri": g.URI, "mimeType": g.MimeType, "text": content}}}, nil
			}
		}
		return nil, failure(-32002, "Resource not found")
	case "tools/list":
		return map[string]any{"tools": s.tools()}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.Name == "" {
			return nil, failure(-32602, "A tool name is required")
		}
		return s.call(ctx, params.Name, params.Arguments)
	default:
		return nil, failure(-32601, "Method not found")
	}
}

func decodeArguments(raw json.RawMessage, target any) bool {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

func toolResult(content string, isError bool) any {
	return map[string]any{"content": []any{map[string]string{"type": "text", "text": content}}, "isError": isError}
}
