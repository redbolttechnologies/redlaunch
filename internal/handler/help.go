package handler

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	documentation "redlaunch"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type helpGuide struct {
	Slug, Title, Description, File string
	Content                        template.HTML
	TOC                            []helpHeading
}

type helpHeading struct {
	ID, Title string
	Level     int
}

type helpLink struct {
	Title, Description, URL string
}

type helpSection struct {
	ID, Title, Description string
	Links                  []helpLink
}

type helpPageData struct {
	Guides   []helpGuide
	Guide    *helpGuide
	Sections []helpSection
}

var helpGuides = []helpGuide{
	{Slug: "installation", Title: "Installation", Description: "Prepare a VPS, configure Google login, and complete setup.", File: "INSTALL.md"},
	{Slug: "reference", Title: "Redlaunch reference", Description: "Features, configuration, project layout, and local development.", File: "README.md"},
	{Slug: "recipes", Title: "Deployment recipes", Description: "Complete GitHub Actions workflows for images, migrations, and deployments.", File: "RECIPES.md"},
	{Slug: "ssh-keys", Title: "Server SSH keys", Description: "Create, use, rotate, and revoke automation keys.", File: "SSH_KEYS.md"},
	{Slug: "api-tokens", Title: "API tokens", Description: "Trigger one-off service runs and poll their status from CI.", File: "API_TOKENS.md"},
	{Slug: "mcp", Title: "Using Redlaunch MCP", Description: "Set up Codex, OpenCode, or Claude Code and ask for help with Redlaunch workflows.", File: "MCP.md"},
}

var helpSections = []helpSection{
	{ID: "getting-started", Title: "Getting started", Description: "Install Redlaunch and connect to your server.", Links: []helpLink{
		{"Install on Ubuntu or Debian", "Docker prerequisites, the installer, and Google OAuth configuration.", "/help/installation"},
		{"Configure Google login", "Register the exact local and public callback URLs.", "/help/installation#2-create-google-oauth-credentials"},
		{"Complete first-run setup", "Choose Caddy, a local registry, or both.", "/help/installation#4-complete-the-redlaunch-first-run-setup"},
	}},
	{ID: "first-steps", Title: "First steps", Description: "Create your first application and make it available.", Links: []helpLink{
		{"Create an application", "Choose a name and a folder for its managed Compose project.", "/help/installation#create-an-application"},
		{"Add services and configuration", "Create application, PostgreSQL, and Redis services; import Compose and environment files.", "/help/installation#create-services"},
		{"Connect a domain", "Add a hostname, then route traffic to a running service.", "/help/installation#add-one-or-more-domains"},
		{"Understand project files", "Learn where Compose files, variables, secrets, and core components live.", "/help/reference#first-steps"},
	}},
	{ID: "how-to", Title: "How-to guides", Description: "Follow practical guides with copyable scripts and workflows.", Links: []helpLink{
		{"How to publish Redlaunch over HTTPS", "Configure the management hostname, deployment mode, and OAuth callback.", "/help/installation#publish-redlaunch-over-https"},
		{"How to push images to the registry", "Build and push images from GitHub Actions through an SSH tunnel.", "/help/recipes#recipe-1-push-docker-images-to-the-local-container-registry"},
		{"How to run database migrations", "Configure a one-off Drizzle service and trigger it through the API or SSH.", "/help/recipes#recipe-2-trigger-a-drizzle-migration-from-github-actions"},
		{"How to deploy through the registry", "Push an image and update the managed service from GitHub Actions.", "/help/recipes#recipe-3-deploy-an-application-from-github-actions-via-the-container-registry"},
		{"How to deploy by copying an image", "Stream a Docker image directly to the server without a registry.", "/help/recipes#recipe-4-deploy-an-application-from-github-actions-by-copying-the-image-directly"},
		{"How to create and revoke SSH keys", "Set up the dedicated SSH user and securely store a CI key.", "/help/ssh-keys#create-a-key"},
		{"How to use API tokens", "Scope a token to an application, dispatch a run, and wait for completion.", "/help/api-tokens#create-a-token"},
		{"How to use Redlaunch MCP", "Check prerequisites and connect Codex, OpenCode, or Claude Code step by step.", "/help/mcp#before-you-start"},
		{"How to connect MCP to your VPS", "Set up a server connection to check task progress and optionally run services.", "/help/mcp#step-4-connect-to-your-vps-when-needed"},
		{"How to update Redlaunch", "Use make update or the Update action in Settings.", "/help/reference#installation"},
	}},
	{ID: "operations", Title: "Operations & troubleshooting", Description: "Maintain the installation and diagnose common problems.", Links: []helpLink{
		{"Back up before a resource migration", "Preserve the database, project files, proxy configuration, and backup schedules.", "/help/installation#before-a-managed-resource-migration"},
		{"Recover interrupted operations", "Understand backup leases and resumable application deletion.", "/help/installation#interrupted-backups-and-deletion-recovery"},
		{"Troubleshoot installation and login", "Resolve OAuth errors, proxy routing, and Docker or systemd issues.", "/help/installation#troubleshooting"},
		{"Troubleshoot CI deployments", "Check SSH access, registry tunnels, Compose identity, and migration runs.", "/help/recipes#troubleshooting-quick-reference"},
		{"Configuration reference", "Review listener, storage, authentication, and metrics settings.", "/help/reference#local-development"},
	}},
}

func (h *Handler) helpPage(w http.ResponseWriter, r *http.Request) {
	data := &helpPageData{Guides: helpGuides, Sections: helpSections}
	if slug := r.PathValue("guide"); slug != "" {
		for _, guide := range helpGuides {
			if guide.Slug != slug {
				continue
			}
			source, err := documentation.Files.ReadFile(guide.File)
			if err == nil {
				guide, err = renderHelpGuide(guide, source)
			}
			if err != nil {
				h.logger.Error("render help guide", "guide", guide.Slug, "error", err)
				http.Error(w, "The help guide could not be read.", http.StatusInternalServerError)
				return
			}
			data.Guide = &guide
			break
		}
		if data.Guide == nil {
			http.NotFound(w, r)
			return
		}
	}
	page := h.shellPageData(r)
	page.ActivePage = "help"
	page.HelpPage = data
	h.writeTemplate(w, "index.html", page)
}

func renderHelpGuide(guide helpGuide, source []byte) (helpGuide, error) {
	markdown := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(helpCodeRenderer{}, 100))),
	)
	doc := markdown.Parser().Parse(text.NewReader(source))
	// The shell owns the page's single h1. Keep the source's section hierarchy.
	if heading, ok := doc.FirstChild().(*ast.Heading); ok && heading.Level == 1 {
		doc.RemoveChild(doc, heading)
	}
	ids := make(map[string]int)
	codeIndex := 0
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Heading:
			title := helpHeadingText(node, source)
			base := helpHeadingID(title)
			id := base
			if count := ids[base]; count > 0 {
				id = fmt.Sprintf("%s-%d", base, count)
			}
			ids[base]++
			node.SetAttributeString("id", []byte(id))
			if node.Level <= 3 {
				guide.TOC = append(guide.TOC, helpHeading{ID: id, Title: title, Level: node.Level})
			}
		case *ast.Link:
			node.Destination = []byte(helpDestination(string(node.Destination)))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			codeIndex++
			n.SetAttributeString("id", []byte(fmt.Sprintf("help-code-%d", codeIndex)))
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return guide, err
	}
	var output bytes.Buffer
	if err := markdown.Renderer().Render(&output, source, doc); err != nil {
		return guide, err
	}
	// Only embedded, versioned documentation reaches this renderer. Goldmark
	// escapes code and disables raw HTML and unsafe links; the renderer below
	// allows only the exact inline <code> tags used by the operator guides.
	guide.Content = template.HTML(output.String())
	return guide, nil
}

func helpHeadingText(n ast.Node, source []byte) string {
	var title strings.Builder
	_ = ast.Walk(n, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch node := child.(type) {
			case *ast.Text:
				title.Write(node.Segment.Value(source))
			case *ast.String:
				title.Write(node.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return html.UnescapeString(title.String())
}

func helpHeadingID(title string) string {
	var id strings.Builder
	for _, char := range strings.ToLower(title) {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || char == '-' || char == '_' {
			id.WriteRune(char)
		} else if unicode.IsSpace(char) {
			id.WriteByte('-')
		}
	}
	return id.String()
}

func helpDestination(destination string) string {
	parsed, err := url.Parse(destination)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" {
		return destination
	}
	for _, guide := range helpGuides {
		if parsed.Path == guide.File {
			parsed.Path = "/help/" + guide.Slug
			return parsed.String()
		}
	}
	// Other repository references (such as sample configuration and LICENSE)
	// are public source files, never filesystem reads on the managed server.
	return "https://github.com/redbolttechnologies/redlaunch/blob/master/" + strings.TrimPrefix(destination, "./")
}

type helpCodeRenderer struct{}

func (helpCodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderHelpCode)
	reg.Register(ast.KindCodeBlock, renderHelpCode)
	reg.Register(ast.KindRawHTML, renderHelpInlineHTML)
}

func renderHelpCode(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	id, _ := n.AttributeString("id")
	language := "text"
	if block, ok := n.(*ast.FencedCodeBlock); ok && len(block.Language(source)) > 0 {
		language = string(block.Language(source))
	}
	_, _ = fmt.Fprintf(w, `<div class="help-code-block"><div class="help-code-toolbar"><span>%s</span><button class="button button-secondary" type="button" data-copy-target="%s" aria-label="Copy %s example"><span>Copy</span></button></div><pre tabindex="0"><code id="%s">`, html.EscapeString(language), id, html.EscapeString(language), id)
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		_, _ = w.WriteString(html.EscapeString(string(line.Value(source))))
	}
	_, _ = w.WriteString("</code></pre></div>\n")
	return ast.WalkSkipChildren, nil
}

func renderHelpInlineHTML(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		node := n.(*ast.RawHTML)
		for i := 0; i < node.Segments.Len(); i++ {
			segment := node.Segments.At(i)
			value := string(segment.Value(source))
			if value == "<code>" || value == "</code>" {
				_, _ = w.WriteString(value)
			}
		}
	}
	return ast.WalkSkipChildren, nil
}
