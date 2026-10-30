package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryReadOnlyConfigInsertion(t *testing.T) {
	config := `version: 0.1
# keep this comment
storage:
    filesystem:
        rootdirectory: /var/lib/registry
http:
    addr: :5000
`
	got, err := RegistryReadOnlyConfig([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "storage:\n    maintenance:\n        readonly:\n            enabled: true\n    filesystem:") {
		t.Fatalf("read-only block missing or misplaced:\n%s", text)
	}
	if !strings.Contains(text, "# keep this comment") {
		t.Fatalf("comment was not preserved:\n%s", text)
	}
}

func TestRegistryReadOnlyConfigUsesDetectedIndent(t *testing.T) {
	config := "storage:\n  filesystem:\n    rootdirectory: /var/lib/registry\n"
	got, err := RegistryReadOnlyConfig([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "storage:\n  maintenance:\n      readonly:\n          enabled: true\n  filesystem:") {
		t.Fatalf("wrong indentation:\n%s", got)
	}
}

func TestRegistryReadOnlyConfigRejectsUnsupportedLayouts(t *testing.T) {
	for name, config := range map[string]string{
		"missing storage":      "version: 0.1\nhttp:\n  addr: :5000\n",
		"empty storage":        "storage:\nhttp:\n  addr: :5000\n",
		"existing maintenance": "storage:\n    maintenance:\n        uploadpurging:\n            enabled: true\n    filesystem:\n        rootdirectory: /var/lib/registry\n",
		"nested storage":       "outer:\n  storage:\n    filesystem:\n      rootdirectory: /var/lib/registry\n",
		"non-filesystem":       "storage:\n    s3:\n        bucket: example\n",
	} {
		if _, err := RegistryReadOnlyConfig([]byte(config)); err == nil {
			t.Fatalf("%s config accepted", name)
		}
	}
}

func TestParseHTTPStatus(t *testing.T) {
	if got := parseHTTPStatus("  HTTP/1.1 405 Method Not Allowed\n"); got != 405 {
		t.Fatalf("status = %d, want 405", got)
	}
	if got := parseHTTPStatus("garbage\n"); got != 0 {
		t.Fatalf("status = %d, want 0", got)
	}
}

func TestRegistryContainerTargetReadsConfig(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
if [ "$1" = "inspect" ]; then
  printf '%s\n' '[{"Id":"abc","State":{"Running":true},"Config":{"Cmd":["/etc/distribution/config.yml"],"Env":["PATH=/bin"]},"Mounts":[]}]'
  exit 0
fi
if [ "$1" = "exec" ]; then
  printf '%s\n' 'version: 0.1
storage:
    filesystem:
        rootdirectory: /var/lib/registry'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	target, err := (CommandRunner{Binary: binary}).RegistryContainerTarget(t.Context(), "registry")
	if err != nil {
		t.Fatal(err)
	}
	if target.ContainerID != "abc" || target.ConfigPath != "/etc/distribution/config.yml" || !target.WasRunning {
		t.Fatalf("target = %#v", target)
	}
	if !strings.Contains(string(target.Config), "rootdirectory") {
		t.Fatalf("config = %q", target.Config)
	}
}

func TestRegistryContainerTargetRejectsEnvOverridesAndMountedConfig(t *testing.T) {
	for name, inspectJSON := range map[string]string{
		"env":   `[{"Id":"abc","State":{"Running":true},"Config":{"Cmd":["/etc/distribution/config.yml"],"Env":["REGISTRY_STORAGE=evil"]},"Mounts":[]}]`,
		"mount": `[{"Id":"abc","State":{"Running":true},"Config":{"Cmd":["/etc/distribution/config.yml"],"Env":[]},"Mounts":[{"Destination":"/etc"}]}]`,
	} {
		binary := filepath.Join(t.TempDir(), "docker")
		script := `#!/bin/sh
if [ "$1" = "inspect" ]; then
  printf '%s\n' '` + inspectJSON + `'
  exit 0
fi
if [ "$1" = "exec" ]; then
  printf '%s\n' 'storage:
    filesystem:
        rootdirectory: /var/lib/registry'
  exit 0
fi
exit 1
`
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := (CommandRunner{Binary: binary}).RegistryContainerTarget(t.Context(), "registry"); err == nil {
			t.Fatalf("%s configuration accepted", name)
		}
	}
}
