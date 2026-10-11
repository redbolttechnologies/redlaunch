package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// NewRemote reuses the stdio tool catalog with an in-process machine API
// transport. The caller must authenticate and authorize the credential and
// resource before serving a request. No network transport is used.
func NewRemote(token string, management bool, transport http.RoundTripper) (*Server, error) {
	if transport == nil {
		return nil, errors.New("remote MCP requires a machine API transport")
	}
	client := &apiClient{baseURL: "http://redlaunch.internal", token: token,
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s := &Server{}
	if management {
		s.management, s.allowManage = client, true
	} else {
		s.client, s.allowRun = client, true
	}
	return s, nil
}

// SupportedHTTPVersion reports the Streamable HTTP protocol versions served.
func SupportedHTTPVersion(version string) bool {
	switch version {
	case "2025-03-26", "2025-06-18", protocolVersion:
		return true
	}
	return false
}

// ServeHTTP implements stateless Streamable HTTP: one JSON-RPC message per
// POST, JSON responses, and no session IDs or server-initiated SSE stream.
// Authentication and Origin validation belong to the enclosing HTTP handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Only POST is supported.", http.StatusMethodNotAllowed)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "Content-Type must be application/json.", http.StatusUnsupportedMediaType)
		return
	}
	if !acceptsMCP(r.Header.Get("Accept")) {
		http.Error(w, "Accept must include application/json and text/event-stream.", http.StatusNotAcceptable)
		return
	}
	version := r.Header.Get("MCP-Protocol-Version")
	if version != "" && !SupportedHTTPVersion(version) {
		http.Error(w, "Unsupported MCP protocol version.", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageBytes))
	if err != nil {
		http.Error(w, "MCP message exceeds the size limit.", http.StatusRequestEntityTooLarge)
		return
	}
	var req request
	var result any
	var rpcErr *rpcError
	id := json.RawMessage("null")
	if !json.Valid(body) {
		rpcErr = failure(-32700, "Parse error")
	} else if json.Unmarshal(body, &req) != nil || req.JSONRPC != "2.0" ||
		(len(req.ID) > 0 && !validID(req.ID)) || (len(req.Params) > 0 && req.Params[0] != '{') {
		rpcErr = failure(-32600, "Invalid request")
	} else if len(req.ID) == 0 || (req.Method == "" && (len(req.Result) > 0 || len(req.Error) > 0)) {
		// Notifications and client responses never execute tools.
		if req.Method == "" && len(req.Result) == 0 && len(req.Error) == 0 {
			rpcErr = failure(-32600, "Invalid request")
		} else {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	} else {
		id = req.ID
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		switch req.Method {
		case "initialize":
			result, rpcErr = remoteInitialize(req.Params)
		case "ping":
			result = map[string]any{}
		case "":
			rpcErr = failure(-32600, "Invalid request")
		default:
			// No per-client session state is kept. A missing header defaults to
			// 2025-03-26, as required by the Streamable HTTP specification.
			result, rpcErr = s.handle(ctx, req)
		}
	}
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	if rpcErr != nil && (rpcErr.Code == -32700 || rpcErr.Code == -32600) {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = json.NewEncoder(w).Encode(response)
}

func remoteInitialize(raw json.RawMessage) (any, *rpcError) {
	var params struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(raw, &params) != nil || params.ProtocolVersion == "" ||
		params.Capabilities == nil || params.ClientInfo.Name == "" || params.ClientInfo.Version == "" {
		return nil, failure(-32602, "Invalid initialization parameters")
	}
	version := protocolVersion
	if SupportedHTTPVersion(params.ProtocolVersion) {
		version = params.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
		"serverInfo":      map[string]string{"name": "redlaunch", "version": "1.0.0"},
		"instructions":    "This connection is limited to its approved resource. Read the integration guides before changing this server. Never echo credentials. Poll asynchronous jobs; inspect failed or lost jobs before retrying mutations.",
	}, nil
}

func acceptsMCP(value string) bool {
	jsonOK, streamOK := false, false
	for _, part := range strings.Split(value, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if weight, ok := params["q"]; ok {
			q, err := strconv.ParseFloat(weight, 64)
			if err != nil || q <= 0 || q > 1 {
				continue
			}
		}
		if media == "" {
			continue
		}
		jsonOK = jsonOK || media == "application/json"
		streamOK = streamOK || media == "text/event-stream"
	}
	return jsonOK && streamOK
}
