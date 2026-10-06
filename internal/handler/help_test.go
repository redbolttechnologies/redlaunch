package handler

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	documentation "redlaunch"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func TestHelpAvailableBeforeSetupAndInSidebar(t *testing.T) {
	web, err := New(nil, &fakeSetupManager{needsSetup: true})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	web.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/help", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		`<h1 id="page-title">Help</h1>`, "Getting started", "First steps", "How-to guides",
		"Operations &amp; troubleshooting", `data-copy-target="help-install-command"`,
		`data-copy-target="help-tunnel-command"`, `href="/help" aria-current="page"`,
		"How to use Redlaunch MCP", `href="/help/mcp#before-you-start"`,
		"How to connect MCP to your VPS", `href="/help/mcp#step-4-connect-to-your-vps-when-needed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("help missing %q", want)
		}
	}
	if strings.Count(body, "<h1") != 1 || strings.Contains(body, "Set up your server") {
		t.Error("help should render its own single heading before setup")
	}
}

func TestHelpGuidesAndLinks(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	routes := web.Routes()
	pages := make(map[string]string)
	for _, guide := range helpGuides {
		response := httptest.NewRecorder()
		path := "/help/" + guide.Slug
		routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		pages[path] = body
		for _, want := range []string{guide.Title, guide.File, `aria-label="On this page"`, `data-copy-target="help-code-1"`, `href="` + path + `" aria-current="page"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		if guide.Slug == "mcp" {
			for _, want := range []string{
				"Before you start", "Step 1: Prepare the Redlaunch program", "Step 2: Connect your coding assistant",
				"Step 3: Try your first request", "Step 4: Connect to your VPS when needed", "Step 5: Allow service runs when needed",
				`id="codex"`, "~/.codex/config.toml", "mcp_servers.redlaunch", "env_vars",
				`id="opencode"`, "opencode.json", "opencode mcp list", "{env:REDLAUNCH_RUN_TOKEN}",
				`id="claude-code"`, "claude mcp add --transport stdio", ".mcp.json", "${REDLAUNCH_RUN_TOKEN}",
				"redlaunch mcp", "mcpServers", "generate_run_workflow", "REDLAUNCH_RUN_TOKEN", "--allow-run",
				`href="/help/api-tokens"`, `href="/help/recipes"`, "Troubleshooting",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("MCP help missing usage instruction %q", want)
				}
			}
		}
		if strings.Count(body, "<h1") != 1 {
			t.Errorf("%s should have one page heading", path)
		}
		ids := make(map[string]bool)
		for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			if ids[match[1]] {
				t.Errorf("%s has duplicate id %q", path, match[1])
			}
			ids[match[1]] = true
		}
		for _, match := range regexp.MustCompile(`data-copy-target="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			if !ids[match[1]] {
				t.Errorf("%s has missing copy target %q", path, match[1])
			}
		}
	}
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/help", nil))
	pages["/help"] = response.Body.String()
	for path, body := range pages {
		for _, match := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			link, err := url.Parse(html.UnescapeString(match[1]))
			if err != nil {
				t.Fatal(err)
			}
			target := link.Path
			if strings.HasPrefix(match[1], "#") {
				target = path
			} else if !strings.HasPrefix(target, "/help") {
				continue
			}
			targetBody, ok := pages[target]
			if !ok {
				t.Errorf("%s links to missing guide %s", path, target)
			} else if link.Fragment != "" && !strings.Contains(targetBody, `id="`+link.Fragment+`"`) {
				t.Errorf("%s links to missing anchor %s", path, match[1])
			}
		}
	}
}

func TestHelpPreservesEveryEmbeddedCodeExample(t *testing.T) {
	for _, guide := range helpGuides {
		t.Run(guide.Slug, func(t *testing.T) {
			source, err := documentation.Files.ReadFile(guide.File)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := renderHelpGuide(guide, source)
			if err != nil {
				t.Fatal(err)
			}
			doc := goldmark.New().Parser().Parse(text.NewReader(source))
			blocks := 0
			err = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering || (n.Kind() != ast.KindFencedCodeBlock && n.Kind() != ast.KindCodeBlock) {
					return ast.WalkContinue, nil
				}
				blocks++
				var example strings.Builder
				for i := 0; i < n.Lines().Len(); i++ {
					line := n.Lines().At(i)
					example.Write(line.Value(source))
				}
				if !strings.Contains(string(rendered.Content), ">"+html.EscapeString(example.String())+"</code></pre>") {
					t.Errorf("code example %d lost its original content or whitespace", blocks)
				}
				return ast.WalkContinue, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(rendered.Content), "data-copy-target="); got != blocks {
				t.Errorf("copy buttons = %d, code examples = %d", got, blocks)
			}
		})
	}
}

func TestHelpRendererEscapesCodeAndRejectsUnsafeHTML(t *testing.T) {
	source := []byte("# Title\n\n## Use <code>make setup</code>\n\n## Repeat\n\n## Repeat\n\n" +
		"<script>alert('bad')</script>\n\n<img src=x onerror=alert(1)>\n\n" +
		"[bad](javascript:alert%281%29)\n\n[Install](INSTALL.md#create-services)\n\n" +
		"```sh\nprintf '%s' '<script>&</script>'\n${{ secrets.EXAMPLE }}\n```\n\n" +
		"| Name | Value |\n| --- | --- |\n| sample | `value` |\n")
	guide, err := renderHelpGuide(helpGuide{}, source)
	if err != nil {
		t.Fatal(err)
	}
	body := string(guide.Content)
	for _, unsafe := range []string{"<script>", "<img", `href="javascript:`, "onerror="} {
		if strings.Contains(body, unsafe) {
			t.Errorf("rendered unsafe content %q", unsafe)
		}
	}
	for _, want := range []string{`id="use-make-setup"`, `<code>make setup</code>`, `id="repeat-1"`, `href="/help/installation#create-services"`, "&lt;script&gt;&amp;&lt;/script&gt;", "${{ secrets.EXAMPLE }}", "<table>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing rendered content %q", want)
		}
	}
}

func TestHelpUnknownGuideAndAuthentication(t *testing.T) {
	web, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/help/unknown", "/help/secrets.env", "/help/.env", "/help/%2e%2e%2fsecrets.env"} {
		response := httptest.NewRecorder()
		web.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, response.Code)
		}
	}
	web, err = New(nil, &fakeAuthenticationService{enabled: true, validateValid: true, validateEmail: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/help", "/help/recipes", "/help/mcp"} {
		response := httptest.NewRecorder()
		web.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/login?next=") {
			t.Errorf("unauthenticated %s should redirect to login", path)
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
		response = httptest.NewRecorder()
		web.Routes().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("authenticated %s status = %d", path, response.Code)
		}
	}
}
