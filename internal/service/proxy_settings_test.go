package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/store"
)

func TestRenderProxyPoliciesAcrossSharedHostname(t *testing.T) {
	cache := "private, no-store"
	policies := map[int64]application.ProxySettings{0: {Compression: &application.ProxyCompression{Gzip: true, Zstandard: true, Brotli: true, MinimumLength: 512}, CacheControl: &cache, SecurityHeaders: &application.ProxySecurityHeaders{ContentTypeOptions: "nosniff", ContentSecurityPolicy: `default-src 'self'; report-uri "/report"`}, BodyLimit: &application.ProxyBodyLimit{Bytes: 4096}, Timeouts: &application.ProxyTimeouts{Dial: "3s", ResponseHeader: "30s", Read: "1m", Write: "1m"}}, 2: {Compression: &application.ProxyCompression{}, BodyLimit: &application.ProxyBodyLimit{}, CacheControl: new(string)}}
	routes := []application.Routing{{ID: 1, ApplicationID: 1, DomainName: "example.com", Path: "/one", ServiceName: "one", ServicePort: 80}, {ID: 2, ApplicationID: 2, DomainName: "example.com", Path: "/two", ServiceName: "two", ServicePort: 80}}
	got := renderCaddyfileWithSettings(routes, []application.RedlaunchDomain{{Name: "admin.example.com"}}, 8080, policies)
	for _, want := range []string{"encode zstd br gzip", "minimum_length 512", "header >Cache-Control \"private, no-store\"", "header >X-Content-Type-Options \"nosniff\"", "max_size 4096", "dial_timeout 3s", "response_header_timeout 30s", "read_timeout 1m", "write_timeout 1m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "encode ") != 1 || strings.Count(got, "Cache-Control") != 1 || strings.Count(got, "max_size") != 1 {
		t.Fatalf("overrides or management isolation failed:\n%s", got)
	}
	if path := os.Getenv("REDLAUNCH_PROXY_POLICY_FIXTURE"); path != "" {
		if err := os.WriteFile(path, []byte(got), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSaveProxySettingsRollback(t *testing.T) {
	root := t.TempDir()
	prepareRoutingProxy(t, root)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app, err := db.Create(t.Context(), application.Application{Name: "Policy", FolderName: "policy"})
	if err != nil {
		t.Fatal(err)
	}
	domain, err := db.CreateDomain(t.Context(), application.Domain{ApplicationID: app.ID, Name: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateRouting(t.Context(), application.Routing{ApplicationID: app.ID, DomainID: domain.ID, Path: "/", ServiceName: "web", ServicePort: 80, ServicePath: "/"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &serviceRuntimeRunner{}
	s, err := NewApplications(db, root, runner)
	if err != nil {
		t.Fatal(err)
	}
	cache := "private, no-store"
	if err := s.SaveProxySettings(t.Context(), 0, application.ProxySettings{CacheControl: &cache}); err != nil {
		t.Fatal(err)
	}
	caddyPath := filepath.Join(root, coreDir, proxyDir, "Caddyfile")
	before := readServiceFile(t, caddyPath)
	runner.reloadErr = errors.New("reload failed")
	public := "public, max-age=3600"
	if err := s.SaveProxySettings(t.Context(), app.ID, application.ProxySettings{CacheControl: &public}); err == nil {
		t.Fatal("expected reload failure")
	}
	p, global, err := s.GetProxySettings(t.Context(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.CacheControl != nil || *global.CacheControl != cache || readServiceFile(t, caddyPath) != before {
		t.Fatal("failed apply did not restore previous policy and Caddyfile")
	}
	if err := s.SaveProxySettings(t.Context(), 99999, application.ProxySettings{}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("missing app: %v", err)
	}
}

func TestEnableProxyBrotli(t *testing.T) {
	directory := t.TempDir()
	updated, changed, err := enableProxyBrotli(directory, proxyCompose)
	if err != nil || !changed {
		t.Fatalf("enable Brotli: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(updated, "dockerfile: Dockerfile.brotli") || !strings.Contains(updated, "redlaunch.managed=true") || !strings.Contains(updated, "secrets.env") {
		t.Fatal("missing build or managed container settings")
	}
	if got := readServiceFile(t, filepath.Join(directory, "Dockerfile.brotli.dockerignore")); got != "*\n" {
		t.Fatalf("build context includes project files: %q", got)
	}
	_, changed, err = enableProxyBrotli(directory, updated)
	if err != nil || changed {
		t.Fatalf("upgrade not idempotent: %v", err)
	}
	if _, _, err := enableProxyBrotli(directory, "services:\n  proxy:\n    image: custom:latest\n"); err == nil {
		t.Fatal("replaced custom image")
	}
	if dir := os.Getenv("REDLAUNCH_PROXY_COMPOSE_FIXTURE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"compose.yml": updated, "Dockerfile.brotli": brotliProxyDockerfile, "Dockerfile.brotli.dockerignore": "*\n", "vars.env": "", "secrets.env": ""} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type proxyBuildRunner struct {
	serviceRuntimeRunner
	upCalls int
	upErr   error
}

func (r *proxyBuildRunner) Up(context.Context, string) error { r.upCalls++; return r.upErr }

func TestSaveBrotliSettingsBuildsBeforeRoutesExist(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) {
			root := t.TempDir()
			prepareRoutingProxy(t, root)
			composePath := filepath.Join(root, coreDir, proxyDir, "compose.yaml")
			if err := os.WriteFile(composePath, []byte(proxyCompose), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "app.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			runner := &proxyBuildRunner{}
			if fail {
				runner.upErr = errors.New("build failed")
			}
			s, err := NewApplications(db, root, runner)
			if err != nil {
				t.Fatal(err)
			}
			err = s.SaveProxySettings(t.Context(), 0, application.ProxySettings{Compression: &application.ProxyCompression{Brotli: true, MinimumLength: 512}})
			if (err != nil) != fail || runner.upCalls != 1 {
				t.Fatalf("apply error=%v upCalls=%d", err, runner.upCalls)
			}
			p, _, err := s.GetProxySettings(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if p.Compression != nil || readServiceFile(t, composePath) != proxyCompose {
					t.Fatal("failed build did not restore policy and Compose")
				}
			} else if p.Compression == nil || !p.Compression.Brotli || !strings.Contains(readServiceFile(t, composePath), "pull_policy: build") {
				t.Fatal("Brotli was not provisioned before routing")
			}
		})
	}
}
