package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryManifestReferencesReadsOnlyManifestBlob(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	digest := "sha256:" + strings.Repeat("a", 64)
	child := "sha256:" + strings.Repeat("b", 64)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf '%s\\n' '" + `{"schemaVersion":2,"manifests":[{"digest":"` + child + `"}]}` + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	refs, err := (CommandRunner{Binary: binary}).RegistryManifestReferences(t.Context(), "redbolt-registry", "app", digest)
	if err != nil || !reflect.DeepEqual(refs, []string{child}) {
		t.Fatalf("references = %v, %v", refs, err)
	}
	args := strings.Join(strings.Fields(readTestArguments(t, binary)), " ")
	want := "exec redbolt-registry cat -- /var/lib/registry/docker/registry/v2/blobs/sha256/aa/" + strings.Repeat("a", 64) + "/data"
	if args != want {
		t.Fatalf("arguments = %q, want %q", args, want)
	}
}
func TestRegistryManifestReferencesRejectsMalformedManifests(t *testing.T) {
	for _, output := range []string{`invalid`, `{"schemaVersion":1}`, `{"schemaVersion":2,"manifests":[{"digest":"../escape"}]}`} {
		binary := filepath.Join(t.TempDir(), "docker")
		script := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := (CommandRunner{Binary: binary}).RegistryManifestReferences(t.Context(), "redbolt-registry", "app", "sha256:"+strings.Repeat("a", 64)); err == nil {
			t.Fatalf("accepted %s", output)
		}
	}
}
func TestDeleteRegistryVersionRemovesRevisionAndCurrentTagsOnly(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	writeRegistryFixture(t, root, "app/_manifests/tags/old/current/link", digest)
	writeRegistryFixture(t, root, "app/_manifests/tags/alias/current/link", digest)
	writeRegistryFixture(t, root, "app/_manifests/tags/moved/current/link", "sha256:"+strings.Repeat("b", 64))
	writeRegistryFixture(t, root, "app/_manifests/revisions/sha256/"+strings.Repeat("a", 64)+"/link", digest)
	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "${0%/*}/args"
if [ "$1" = "inspect" ]; then exit 0; fi
if [ "$1" = "cp" ]; then
 mkdir -p "$3/repositories"
 cp -r 'FIXTURE/.' "$3/repositories/"
 exit 0
fi
if [ "$1" = "exec" ]; then exit 0; fi
exit 1
`
	script = strings.ReplaceAll(script, "FIXTURE", root)
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := (CommandRunner{Binary: binary}).DeleteRegistryVersion(t.Context(), "redbolt-registry", "app", digest); err != nil {
		t.Fatal(err)
	}
	args := readTestArguments(t, binary)
	for _, path := range []string{"tags/old", "tags/alias", "revisions/sha256/" + strings.Repeat("a", 64)} {
		want := "exec\nredbolt-registry\nrm\n-rf\n--\n" + registryRepositoriesPath + "/app/_manifests/" + path + "\n"
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %q", want, args)
		}
	}
	if strings.Contains(args, "/tags/moved") || strings.Contains(args, "/blobs/") {
		t.Fatalf("unsafe deletion: %s", args)
	}
}
func TestRegistryVersionOperationsRejectUnsafePaths(t *testing.T) {
	runner := CommandRunner{Binary: filepath.Join(t.TempDir(), "must-not-execute")}
	for _, tc := range []struct{ container, repository, digest string }{
		{"", "app", "sha256:" + strings.Repeat("a", 64)},
		{"registry", "../escape", "sha256:" + strings.Repeat("a", 64)},
		{"registry", "app;touch bad", "sha256:" + strings.Repeat("a", 64)},
		{"registry", "app", "sha256:../../escape"},
		{"registry", "app", "sha256:" + strings.Repeat("x", 64)},
	} {
		if err := validateRegistryVersion(tc.container, tc.repository, tc.digest); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
		if _, err := runner.RegistryManifestReferences(context.Background(), tc.container, tc.repository, tc.digest); err == nil {
			t.Fatal("read accepted unsafe path")
		}
		if err := runner.DeleteRegistryVersion(context.Background(), tc.container, tc.repository, tc.digest); err == nil {
			t.Fatal("delete accepted unsafe path")
		}
	}
}
func TestRegistryContentsRejectsTruncatedSnapshot(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	for i := 0; i < maxRegistryImages+1; i++ {
		writeRegistryFixture(t, root, fmt.Sprintf("app/_manifests/tags/tag%d/current/link", i), digest)
	}
	if _, err := readRegistryContents(root); err == nil {
		t.Fatal("truncated inventory must fail before retention can delete anything")
	}
}
