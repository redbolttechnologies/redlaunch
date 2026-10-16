package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type interopOAuthService struct {
	oauthService
	refreshes atomic.Int32
}

type interopAPITokenService struct {
	apiTokenService
	firstID     atomic.Int64
	expireFirst atomic.Bool
}

func (s *interopAPITokenService) Authenticate(ctx context.Context, token string) (application.APIToken, error) {
	item, err := s.apiTokenService.Authenticate(ctx, token)
	if err == nil {
		s.firstID.CompareAndSwap(0, item.ID)
		if s.expireFirst.Load() && item.ID == s.firstID.Load() {
			return application.APIToken{}, application.ErrAPITokenExpired
		}
	}
	return item, err
}

func (s *interopOAuthService) ExchangeToken(ctx context.Context, request application.OAuthTokenRequest) (application.OAuthTokenResponse, error) {
	response, err := s.oauthService.ExchangeToken(ctx, request)
	if err == nil {
		// Short advertised lifetime exercises the client's refresh logic without
		// sleeping for an hour or changing production token policy.
		response.ExpiresIn = 1
		if request.GrantType == application.OAuthGrantTypeRefreshToken {
			s.refreshes.Add(1)
		}
	}
	return response, err
}

func TestMCPOpenCodeInterop(t *testing.T) {
	for _, scope := range []string{"manage", "run"} {
		t.Run(scope, func(t *testing.T) { testMCPOpenCodeInterop(t, scope) })
	}
}

func testMCPOpenCodeInterop(t *testing.T, scope string) {
	if os.Getenv("REDLAUNCH_MCP_INTEROP_TEST") != "1" {
		t.Skip("set REDLAUNCH_MCP_INTEROP_TEST=1 to test installed MCP clients")
	}
	binary, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("OpenCode is not installed")
	}
	f := newOAuthHandlerFixture(t)
	oauth := &interopOAuthService{oauthService: f.web.oauth}
	f.web.oauth = oauth
	apiTokens := &interopAPITokenService{apiTokenService: f.web.apiTokens}
	f.web.apiTokens = apiTokens
	server := httptest.NewServer(f.web.Routes())
	t.Cleanup(server.Close)
	home := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Dir = home
	cmd.Env = interopEnvironment(home)
	cmd.Env = append(cmd.Env, "OPENCODE_SERVER_PASSWORD=interop-test-only")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second, Transport: interopLocationTransport{directory: home}}
	for {
		resp, err := client.Get(base + "/api/info")
		if err == nil {
			resp.Body.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("OpenCode server did not start")
		case <-time.After(100 * time.Millisecond):
		}
	}
	var servers []struct {
		Name          string `json:"name"`
		IntegrationID string `json:"integrationID"`
		Status        struct {
			Type string `json:"status"`
		} `json:"status"`
	}
	resource := "/mcp/manage"
	if scope == "run" {
		resource = "/mcp/apps/1"
	}
	interopAPI(t, client, "PUT", base+"/api/experimental/mcp/redlaunch", map[string]any{"config": map[string]any{"type": "remote", "url": server.URL + resource, "oauth": map[string]string{"scope": scope}}}, nil)
	interopAPI(t, client, "GET", base+"/api/mcp", nil, &servers)
	if len(servers) != 1 || servers[0].IntegrationID == "" {
		t.Fatal("OpenCode did not load isolated MCP configuration")
	}
	integration := base + "/api/integration/" + url.PathEscape(servers[0].IntegrationID)
	var info struct {
		Methods []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"methods"`
	}
	interopAPI(t, client, "GET", integration, nil, &info)
	if len(info.Methods) == 0 {
		t.Fatal("OpenCode did not offer OAuth")
	}
	var attempt struct {
		URL       string `json:"url"`
		AttemptID string `json:"attemptID"`
	}
	interopAPI(t, client, "POST", integration+"/connect/oauth", map[string]string{"methodID": info.Methods[0].ID}, &attempt)
	callback := approveInteropConnection(t, f, server.URL, attempt.URL)
	resp, err := client.Get(callback)
	if err != nil {
		t.Fatal("OpenCode callback failed")
	}
	resp.Body.Close()
	var status struct {
		Status string `json:"status"`
	}
	interopAPI(t, client, "GET", integration+"/connect/oauth/"+url.PathEscape(attempt.AttemptID), nil, &status)
	if status.Status != "complete" {
		t.Fatalf("OpenCode OAuth status = %s", status.Status)
	}
	interopAPI(t, client, "POST", base+"/api/experimental/mcp/redlaunch/connect", nil, nil)
	interopAPI(t, client, "GET", base+"/api/mcp", nil, &servers)
	t.Logf("OpenCode MCP status = %s", servers[0].Status.Type)
	if servers[0].Status.Type != "connected" {
		t.Fatal("OpenCode did not connect after OAuth")
	}
	items, err := f.web.oauth.ListConnections(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatal("OpenCode did not create a connection")
	}
	if items[0].Scope != scope || items[0].Resource != server.URL+resource {
		t.Fatal("OpenCode grant does not match the configured resource")
	}
	time.Sleep(1200 * time.Millisecond)
	apiTokens.expireFirst.Store(true)
	interopAPI(t, client, "POST", base+"/api/experimental/mcp/redlaunch/disconnect", nil, nil)
	interopAPI(t, client, "POST", base+"/api/experimental/mcp/redlaunch/connect", nil, nil)
	interopAPI(t, client, "GET", base+"/api/mcp", nil, &servers)
	if servers[0].Status.Type != "connected" || oauth.refreshes.Load() == 0 {
		t.Fatal("OpenCode did not refresh and reconnect")
	}
	if err := f.web.oauth.RevokeConnection(t.Context(), items[0].ID); err != nil {
		t.Fatal(err)
	}
	interopAPI(t, client, "POST", base+"/api/experimental/mcp/redlaunch/disconnect", nil, nil)
	interopAPI(t, client, "POST", base+"/api/experimental/mcp/redlaunch/connect", nil, nil)
	interopAPI(t, client, "GET", base+"/api/mcp", nil, &servers)
	if servers[0].Status.Type == "connected" {
		t.Fatal("OpenCode reconnected with a revoked grant")
	}
	t.Log("OpenCode refresh rotation, reconnection, and revocation passed")
}

type interopLocationTransport struct{ directory string }

func (tr interopLocationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	query := copy.URL.Query()
	query.Set("location[directory]", tr.directory)
	copy.URL.RawQuery = query.Encode()
	return http.DefaultTransport.RoundTrip(copy)
}

func interopAPI(t *testing.T, client *http.Client, method, target string, body any, result any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(data))
	}
	req, _ := http.NewRequest(method, target, reader)
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("opencode", "interop-test-only")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("client API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("client API %s %s status=%d", method, req.URL.Path, resp.StatusCode)
	}
	if result != nil {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) == nil && envelope["data"] != nil {
			data = envelope["data"]
		}
		if err := json.Unmarshal(data, result); err != nil {
			t.Fatal(err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
}

// These opt-in tests use real clients but never a developer's configuration,
// credentials, Docker projects, or model provider. Only disposable approvals
// in the existing HTTP fixture are automated.
func TestMCPCodexInterop(t *testing.T) {
	for _, scope := range []string{"manage", "run"} {
		t.Run(scope, func(t *testing.T) { testMCPCodexInterop(t, scope) })
	}
}

func testMCPCodexInterop(t *testing.T, scope string) {
	if os.Getenv("REDLAUNCH_MCP_INTEROP_TEST") != "1" {
		t.Skip("set REDLAUNCH_MCP_INTEROP_TEST=1 to test installed MCP clients")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex is not installed")
	}
	f := newOAuthHandlerFixture(t)
	oauth := &interopOAuthService{oauthService: f.web.oauth}
	f.web.oauth = oauth
	apiTokens := &interopAPITokenService{apiTokenService: f.web.apiTokens}
	f.web.apiTokens = apiTokens
	server := httptest.NewServer(f.web.Routes())
	t.Cleanup(server.Close)
	home := t.TempDir()
	resource := "/mcp/manage"
	if scope == "run" {
		resource = "/mcp/apps/1"
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("mcp_oauth_credentials_store = \"file\"\n[mcp_servers.redlaunch]\nurl = \""+server.URL+resource+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp", "login", "redlaunch", "--no-browser", "--oauth-client-registration", "dcr", "--scopes", scope)
	cmd.Env = interopEnvironment(home)
	cmd.Dir = home
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard // OAuth output may contain transient credentials.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	approved := false
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := scanner.Text()
		start := strings.Index(line, server.URL+"/oauth/authorize?")
		if start < 0 {
			continue
		}
		target := strings.Fields(line[start:])[0]
		callback := approveInteropConnection(t, f, server.URL, target)
		if _, err := io.WriteString(input, callback+"\n"); err != nil {
			t.Fatal(err)
		}
		approved = true
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Codex OAuth login failed (%v); approved=%v", err, approved)
	}
	if !approved {
		t.Fatal("Codex did not request authorization")
	}
	items, err := f.web.oauth.ListConnections(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatal("Codex did not create a grant")
	}
	if items[0].Scope != scope || items[0].Resource != server.URL+resource {
		t.Fatal("Codex grant does not match the configured resource")
	}
	t.Log("Codex loopback discovery, DCR, S256 PKCE, consent, and code exchange passed")
	time.Sleep(1200 * time.Millisecond)
	apiTokens.expireFirst.Store(true)
	app := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	app.WaitDelay = 2 * time.Second
	app.Env = interopEnvironment(home)
	app.Dir = home
	app.Stderr = io.Discard
	appOut, err := app.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	appIn, err := app.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Process.Kill(); app.Wait() })
	io.WriteString(appIn, `{"id":0,"method":"initialize","params":{"clientInfo":{"name":"redlaunch_interop","version":"1"}}}`+"\n")
	appScanner := bufio.NewScanner(appOut)
	appScanner.Buffer(make([]byte, 4096), 1024*1024)
	for appScanner.Scan() {
		var message struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(appScanner.Bytes(), &message); err != nil {
			continue
		}
		if message.ID == nil {
			continue
		}
		if *message.ID == 0 {
			if message.Error != nil {
				t.Fatal("Codex app-server initialization failed")
			}
			io.WriteString(appIn, `{"method":"initialized"}`+"\n")
			io.WriteString(appIn, `{"id":1,"method":"mcpServerStatus/list","params":{}}`+"\n")
		}
		if *message.ID == 1 {
			if message.Error != nil {
				t.Fatal("Codex MCP status request failed")
			}
			var status struct {
				Data []struct {
					Name  string                     `json:"name"`
					Tools map[string]json.RawMessage `json:"tools"`
				} `json:"data"`
			}
			if err := json.Unmarshal(message.Result, &status); err != nil {
				t.Fatal(err)
			}
			if len(status.Data) != 1 || len(status.Data[0].Tools) == 0 {
				t.Fatal("Codex did not initialize and load MCP tools")
			}
			t.Logf("Codex initialized MCP and loaded %d tools", len(status.Data[0].Tools))
			if oauth.refreshes.Load() == 0 {
				t.Fatal("Codex did not refresh its expired credential")
			}
			t.Log("Codex refresh rotation passed")
			return
		}
	}
	t.Fatal("Codex app-server did not return MCP status")
}

func interopEnvironment(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_CACHE_HOME=" + home + "/cache", "XDG_STATE_HOME=" + home + "/state"}
}

func approveInteropConnection(t *testing.T, f oauthHandlerFixture, origin, target string) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil || u.Scheme+"://"+u.Host != origin || u.Path != "/oauth/authorize" {
		t.Fatal("client authorization URL is not the isolated server")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(f.session)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("client consent request failed: %d", response.StatusCode)
	}
	var csrf *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == csrfCookieName {
			csrf = cookie
		}
	}
	if csrf == nil {
		t.Fatal("consent did not set CSRF cookie")
	}
	form := u.Query()
	form.Set("csrf_token", csrf.Value)
	form.Set("decision", "approve")
	req, _ = http.NewRequest(http.MethodPost, origin+"/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(f.session)
	req.AddCookie(csrf)
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("client consent approval failed: %d", response.StatusCode)
	}
	return response.Header.Get("Location")
}
