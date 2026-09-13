package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	originalLookup := publicIPLookup
	originalContainerized := containerized
	publicIPLookup = func() string { return "" }
	containerized = func() bool { return false }
	code := m.Run()
	publicIPLookup = originalLookup
	containerized = originalContainerized
	os.Exit(code)
}

func TestLooksLikeContainerID(t *testing.T) {
	for _, candidate := range []string{"0732958d40b4", "ABCDEF123456", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if !looksLikeContainerID(candidate) {
			t.Fatalf("looksLikeContainerID(%q) = false, want true", candidate)
		}
	}
	for _, candidate := range []string{"", "web-01", "my-vps", "hostname", "12345", "zzzzzzzzzzzz", "0732958d40b4x", "172.19.0.2"} {
		if looksLikeContainerID(candidate) {
			t.Fatalf("looksLikeContainerID(%q) = true, want false", candidate)
		}
	}
}

func TestIsValidHostnameDisplay(t *testing.T) {
	for _, valid := range []string{"my-vps", "web-01.example.com", "host123", "0732958d40b4"} {
		if !isValidHostnameDisplay(valid) {
			t.Fatalf("isValidHostnameDisplay(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "has space", "has/slash", "has:colon", "has@at", "has[bracket", "a/b", "bad;cmd", "bad|pipe", "bad$var"} {
		if isValidHostnameDisplay(invalid) {
			t.Fatalf("isValidHostnameDisplay(%q) = true, want false", invalid)
		}
	}
}

func TestDiscoverHostnamePrefersEnvOverride(t *testing.T) {
	t.Setenv("HOST_HOSTNAME", "my-vps")
	t.Setenv("METRICS_FILESYSTEM_ROOT", "")
	original := containerized
	containerized = func() bool { return true }
	defer func() { containerized = original }()
	if got := discoverHostname(); got != "my-vps" {
		t.Fatalf("discoverHostname() = %q, want my-vps", got)
	}
}

func TestDiscoverHostnameIgnoresInvalidOverride(t *testing.T) {
	t.Setenv("HOST_HOSTNAME", "bad hostname")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "hostname"), []byte("host-file-name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRICS_FILESYSTEM_ROOT", root)
	original := containerized
	containerized = func() bool { return false }
	defer func() { containerized = original }()
	if got := discoverHostname(); got != "host-file-name" {
		t.Fatalf("discoverHostname() = %q, want host-file-name", got)
	}
}

func TestDiscoverHostnameReadsHostFile(t *testing.T) {
	t.Setenv("HOST_HOSTNAME", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "hostname"), []byte("  file-host  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRICS_FILESYSTEM_ROOT", root)
	original := containerized
	containerized = func() bool { return false }
	defer func() { containerized = original }()
	if got := discoverHostname(); got != "file-host" {
		t.Fatalf("discoverHostname() = %q, want file-host", got)
	}
}

func TestDiscoverIPAddressPrefersEnvOverride(t *testing.T) {
	t.Setenv("HOST_PUBLIC_IP", "203.0.113.10")
	originalLookup := publicIPLookup
	publicIPLookup = func() string { return "198.51.100.7" }
	defer func() { publicIPLookup = originalLookup }()
	originalContainerized := containerized
	containerized = func() bool { return true }
	defer func() { containerized = originalContainerized }()
	if got := discoverIPAddress(); got != "203.0.113.10" {
		t.Fatalf("discoverIPAddress() = %q, want 203.0.113.10", got)
	}
}

func TestDiscoverIPAddressUsesPublicLookup(t *testing.T) {
	t.Setenv("HOST_PUBLIC_IP", "")
	originalLookup := publicIPLookup
	publicIPLookup = func() string { return "203.0.113.10" }
	defer func() { publicIPLookup = originalLookup }()
	originalContainerized := containerized
	containerized = func() bool { return true }
	defer func() { containerized = originalContainerized }()
	if got := discoverIPAddress(); got != "203.0.113.10" {
		t.Fatalf("discoverIPAddress() = %q, want 203.0.113.10", got)
	}
}

func TestDiscoverIPAddressAvoidsContainerInterfaceAddress(t *testing.T) {
	t.Setenv("HOST_PUBLIC_IP", "")
	originalLookup := publicIPLookup
	publicIPLookup = func() string { return "" }
	defer func() { publicIPLookup = originalLookup }()
	originalContainerized := containerized
	containerized = func() bool { return true }
	defer func() { containerized = originalContainerized }()
	if got := discoverIPAddress(); got != "" {
		t.Fatalf("discoverIPAddress() in container without public IP = %q, want empty", got)
	}
}

func TestFetchPublicIPFromTestServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.10\n"))
	}))
	defer server.Close()
	if got := fetchPublicIPFrom(server.URL); got != "203.0.113.10" {
		t.Fatalf("fetchPublicIPFrom() = %q, want 203.0.113.10", got)
	}
}

func TestFetchPublicIPFromRejectsNonIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-an-ip"))
	}))
	defer server.Close()
	if got := fetchPublicIPFrom(server.URL); got != "" {
		t.Fatalf("fetchPublicIPFrom(non-IP) = %q, want empty", got)
	}
}

func TestDiscoverInterfaceIPAddressReturnsValidIPOrEmpty(t *testing.T) {
	got := discoverInterfaceIPAddress()
	if got == "" {
		return
	}
	if parsed := net.ParseIP(got); parsed == nil {
		t.Fatalf("discoverInterfaceIPAddress() = %q, want valid IP or empty", got)
	}
}
