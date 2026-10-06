package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"redlaunch/internal/application"
)

// NewWithManagement adds an independent server-wide credential. Existing run
// tokens and --allow-run never enable management tools.
func NewWithManagement(baseURL, runToken, managementToken string, allowRun, allowManage bool) (*Server, error) {
	runURL := baseURL
	if managementToken != "" && runToken == "" {
		runURL = ""
	}
	s, err := New(runURL, runToken, allowRun)
	if err != nil {
		return nil, err
	}
	if managementToken != "" {
		s.management, err = newAPIClient(baseURL, managementToken)
		if err != nil {
			return nil, errors.New("invalid management API configuration: provide REDLAUNCH_URL and a valid REDLAUNCH_MANAGEMENT_TOKEN using HTTPS or loopback HTTP")
		}
	}
	if allowManage && s.management == nil {
		return nil, errors.New("--allow-manage requires REDLAUNCH_URL and REDLAUNCH_MANAGEMENT_TOKEN")
	}
	s.allowManage = allowManage
	return s, nil
}

func (s *Server) managementTools() []tool {
	if s.management == nil {
		return nil
	}
	var result []tool
	for _, op := range application.ManagementOperations() {
		if !op.ReadOnly && !s.allowManage {
			continue
		}
		required := op.Required
		if required == nil {
			required = []string{}
		}
		result = append(result, tool{op.Name, op.Description + " Mutations return job_id; poll get_management_job. Failed jobs return a generic error without sensitive output.", schema(op.Properties, required...), map[string]any{"readOnlyHint": op.ReadOnly, "destructiveHint": !op.ReadOnly, "idempotentHint": op.ReadOnly, "openWorldHint": true}})
	}
	result = append(result, tool{"get_management_job", "Poll a management job; returns status and sanitized result. Jobs expire one hour after completion and disappear on server restart.", schema(map[string]any{"job_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "job_id"), map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}})
	return result
}

func (s *Server) callManagement(ctx context.Context, name string, raw json.RawMessage) (any, *rpcError) {
	path := "/api/v1/management/" + name
	method := http.MethodPost
	if name == "get_management_job" {
		var args struct {
			JobID string `json:"job_id"`
		}
		if !decodeArguments(raw, &args) || !validJobID(args.JobID) {
			return nil, failure(-32602, "Invalid management job_id")
		}
		method = http.MethodGet
		path = "/api/v1/management/jobs/" + url.PathEscape(args.JobID)
		raw = nil
	} else {
		op, ok := application.ManagementOperationByName(name)
		if !ok {
			return nil, failure(-32602, "Unknown management operation")
		}
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		if _, err := application.DecodeManagementArguments(op, raw); err != nil {
			return nil, failure(-32602, "Invalid management arguments")
		}
	}
	result, err := s.management.managementRequest(ctx, method, path, raw)
	if err != nil {
		return toolResult(err.Error(), true), nil
	}
	var response struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(result, &response)
	return toolResult(string(result), response.Status == "failed"), nil
}

func (c *apiClient) managementRequest(ctx context.Context, method, path string, raw []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("could not construct Redlaunch API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("Redlaunch API request failed; a dispatched operation may still be running. Inspect before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("Redlaunch API returned HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(content) > 65536 {
		return nil, errors.New("invalid or oversized Redlaunch API response")
	}
	var result struct {
		Status string          `json:"status"`
		JobID  string          `json:"job_id,omitempty"`
		Result json.RawMessage `json:"result,omitempty"`
	}
	if json.Unmarshal(content, &result) != nil {
		return nil, errors.New("invalid Redlaunch API response")
	}
	switch result.Status {
	case "running":
		if response.StatusCode == http.StatusAccepted && !validJobID(result.JobID) {
			return nil, errors.New("invalid management job ID")
		}
	case "complete":
	case "failed":
		// Discard upstream error text, just as the run client does.
		result.Result = nil
	default:
		return nil, errors.New("invalid management job status")
	}
	if bytes.Contains(content, []byte(c.token)) {
		return nil, errors.New("invalid Redlaunch API response")
	}
	return json.Marshal(result)
}
