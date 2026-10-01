package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

// apiClient is an adapter for the existing machine API, not a Docker or
// database interface. Its destination is fixed by the operator at startup.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient(baseURL, token string) (*apiClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("REDLAUNCH_URL must be an HTTPS origin or loopback HTTP origin, without credentials, path, query, or fragment")
	}
	if parsed.Scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		// IP literals avoid hostname resolution changing a plaintext destination.
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("plain HTTP requires a loopback IP address (for example http://127.0.0.1:8080)")
		}
	}
	cleanToken, ok := application.SplitAPIToken(token)
	if !ok {
		return nil, errors.New("REDLAUNCH_RUN_TOKEN must contain a valid Redlaunch API token")
	}
	return &apiClient{
		baseURL: strings.TrimSuffix(parsed.String(), "/"), token: cleanToken,
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

type runResponse struct {
	Status string `json:"status"`
	JobID  string `json:"job_id,omitempty"`
}

func runPath(applicationID int64, serviceName string) string {
	return "/api/v1/applications/" + strconv.FormatInt(applicationID, 10) + "/services/" + url.PathEscape(serviceName) + "/run"
}

func (c *apiClient) run(ctx context.Context, applicationID int64, serviceName string) (runResponse, error) {
	return c.request(ctx, http.MethodPost, runPath(applicationID, serviceName), http.StatusAccepted)
}

func (c *apiClient) status(ctx context.Context, applicationID int64, serviceName, jobID string) (runResponse, error) {
	return c.request(ctx, http.MethodGet, runPath(applicationID, serviceName)+"/status?id="+url.QueryEscape(jobID), http.StatusOK)
}

func (c *apiClient) request(ctx context.Context, method, path string, expectedStatus int) (runResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return runResponse{}, errors.New("could not construct Redlaunch API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return runResponse{}, errors.New("Redlaunch API request failed; check connectivity or timeout")
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		// Never forward arbitrary upstream bodies or transport errors: they may
		// include credentials, HTML, or sensitive service output.
		return runResponse{}, fmt.Errorf("Redlaunch API returned HTTP %d; see the api-tokens guide for status codes", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(content) > 65536 {
		return runResponse{}, errors.New("invalid or oversized Redlaunch API response")
	}
	var result runResponse
	if json.Unmarshal(content, &result) != nil {
		return runResponse{}, errors.New("invalid Redlaunch API JSON response")
	}
	switch result.Status {
	case "running", "complete", "failed":
	default:
		return runResponse{}, errors.New("invalid Redlaunch job status")
	}
	if method == http.MethodPost {
		if result.Status != "running" || !validJobID(result.JobID) || strings.Contains(result.JobID, c.token) {
			return runResponse{}, errors.New("invalid Redlaunch dispatch response")
		}
	} else {
		result.JobID = ""
	}
	return result, nil
}

func validJobID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
