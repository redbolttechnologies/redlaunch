package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistryFixture(t *testing.T, root, linkPath, digest string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(linkPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(digest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func registryFixtureTree(t *testing.T, root string) {
	t.Helper()
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	digestC := "sha256:" + strings.Repeat("c", 64)
	writeRegistryFixture(t, root, "status-page/web/_manifests/tags/abc123/current/link", digestA)
	writeRegistryFixture(t, root, "status-page/web/_manifests/tags/def456/current/link", digestB)
	writeRegistryFixture(t, root, "admin/_manifests/tags/latest/current/link", digestC)
	// A revision link is part of the layout but never a pushed tag.
	writeRegistryFixture(t, root, "status-page/web/_manifests/revisions/sha256/"+strings.Repeat("d", 64)+"/link", "sha256:"+strings.Repeat("e", 64))
	// Invalid names, tags, and digests must be skipped, not fail the listing.
	writeRegistryFixture(t, root, "BAD NAME/_manifests/tags/good/current/link", digestA)
	writeRegistryFixture(t, root, "ok/_manifests/tags/-bad/current/link", digestA)
	writeRegistryFixture(t, root, "ok2/_manifests/tags/good/current/link", "not-a-digest")
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(string(filepath.Separator)+"etc", filepath.Join(root, "evil")); err != nil {
		t.Fatal(err)
	}
}

func TestListRegistryContentsReadsPushedTags(t *testing.T) {
	fixture := t.TempDir()
	registryFixtureTree(t, filepath.Join(fixture, "repos"))
	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
printf '%s
' "$@" >> "${0%/*}/args"
if [ "$1" = "inspect" ]; then printf '{}
'; exit 0; fi
if [ "$1" = "cp" ]; then
  mkdir -p "$3/repositories"
  cp -r "${FIXTURE}/." "$3/repositories/"
  exit 0
fi
exit 1
`
	script = strings.ReplaceAll(script, "${FIXTURE}", filepath.Join(fixture, "repos"))
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := (CommandRunner{Binary: binary}).ListRegistryContents(context.Background(), "redbolt-registry")
	if err != nil {
		t.Fatal(err)
	}
	want := []RegistryContent{
		{Repository: "status-page/web", Tag: "abc123", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Repository: "status-page/web", Tag: "def456", Digest: "sha256:" + strings.Repeat("b", 64)},
		{Repository: "admin", Tag: "latest", Digest: "sha256:" + strings.Repeat("c", 64)},
	}
	if len(got) != len(want) {
		t.Fatalf("ListRegistryContents() = %#v, want %#v", got, want)
	}
	byReference := make(map[string]string, len(got))
	for _, content := range got {
		byReference[content.Repository+":"+content.Tag] = content.Digest
	}
	for _, content := range want {
		if byReference[content.Repository+":"+content.Tag] != content.Digest {
			t.Fatalf("ListRegistryContents() = %#v, want %#v", got, want)
		}
	}

	args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(strings.Split(strings.TrimSpace(string(args)), "\n"), "\x00")
	if !strings.Contains(joined, "inspect\x00redbolt-registry") {
		t.Fatalf("registry arguments = %q, want explicit inspect", joined)
	}
	if !strings.Contains(joined, "cp\x00redbolt-registry:/var/lib/registry/docker/registry/v2/repositories") {
		t.Fatalf("registry arguments = %q, want explicit cp of the repositories tree", joined)
	}
}

func TestListRegistryContentsReportsMissingContainerAsUnavailable(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\necho 'Error: No such container' >&2\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := (CommandRunner{Binary: binary}).ListRegistryContents(context.Background(), "redbolt-registry")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("ListRegistryContents() error = %v, want %v", err, ErrRegistryUnavailable)
	}
}

func TestListRegistryContentsTreatsFreshRegistryAsEmpty(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nif [ \"$1\" = inspect ]; then exit 0; fi\necho 'Error response from daemon: Could not find the file /var/lib/registry/docker/registry/v2/repositories in container redbolt-registry' >&2\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := (CommandRunner{Binary: binary}).ListRegistryContents(context.Background(), "redbolt-registry")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ListRegistryContents() = %#v, want no contents", got)
	}
}

func TestListRegistryContentsKeepsCopyErrors(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nif [ \"$1\" = inspect ]; then exit 0; fi\necho 'Error response from daemon: boom' >&2\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := (CommandRunner{Binary: binary}).ListRegistryContents(context.Background(), "redbolt-registry")
	if err == nil || errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("ListRegistryContents() error = %v, want copy failure", err)
	}
}

func TestListRegistryContentsRequiresContainerName(t *testing.T) {
	if _, err := (CommandRunner{Binary: "docker"}).ListRegistryContents(context.Background(), "  "); err == nil {
		t.Fatal("ListRegistryContents() error = nil, want container name error")
	}
}

func TestReadRegistryContentsSkipsUnexpectedLayout(t *testing.T) {
	if _, err := readRegistryContents(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("readRegistryContents(missing) error = %v, want nil", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegistryContents(file); err == nil {
		t.Fatal("readRegistryContents(file) error = nil, want directory error")
	}
}

func TestValidRegistryNames(t *testing.T) {
	for _, value := range []string{"web", "status-page/web", "a.b_c-d/e"} {
		if !validRegistryPathComponent(value[strings.LastIndex(value, "/")+1:]) {
			t.Fatalf("validRegistryPathComponent(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", ".", "..", "-bad", "bad-", "BAD", "has space", "has/slash", "_Command"} {
		if validRegistryPathComponent(value) {
			t.Fatalf("validRegistryPathComponent(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"latest", "v1", "abc123DEF", "1.2-3_x", strings.Repeat("a", 128)} {
		if !validRegistryTag(value) {
			t.Fatalf("validRegistryTag(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "-bad", ".bad", strings.Repeat("a", 129), "has space", "bad/tag"} {
		if validRegistryTag(value) {
			t.Fatalf("validRegistryTag(%q) = true, want false", value)
		}
	}
	if !validRegistryDigest("sha256:" + strings.Repeat("0", 64)) {
		t.Fatal("validRegistryDigest() = false, want true")
	}
	for _, value := range []string{"", "sha256:xyz", "md5:" + strings.Repeat("0", 32), "sha256:" + strings.Repeat("0", 63)} {
		if validRegistryDigest(value) {
			t.Fatalf("validRegistryDigest(%q) = true, want false", value)
		}
	}
}
