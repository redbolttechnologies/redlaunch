package application

import (
	"strings"
	"testing"
	"time"
)

func testRegistryImage(repository, tag, digest string, pushedAt time.Time) RegistryImage {
	return RegistryImage{Repository: repository, Tag: tag, Digest: digest, PushedAt: pushedAt}
}

func TestValidateRegistryDigest(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if got, err := ValidateRegistryDigest("  " + valid + "  "); err != nil || got != valid {
		t.Fatalf("ValidateRegistryDigest(valid) = (%q, %v), want (%q, nil)", got, err, valid)
	}
	for _, digest := range []string{
		"",
		"sha256:",
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"sha512:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("z", 64),
		strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 64) + "/../escape",
	} {
		if _, err := ValidateRegistryDigest(digest); err == nil {
			t.Fatalf("ValidateRegistryDigest(%q) error = nil, want invalid", digest)
		}
	}
}

func TestGroupRegistryVersionsGroupsTagsByDigest(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	now := time.Now()
	older := now.Add(-time.Hour)
	images := []RegistryImage{
		testRegistryImage("acme-app", "build-2", digestA, now),
		testRegistryImage("acme-app", "development", digestA, older),
		testRegistryImage("acme-app", "build-1", digestB, older.Add(-time.Hour)),
		testRegistryImage("other-app", "build-9", digestB, now),
		testRegistryImage("acme-app", "broken", "", now),
	}

	versions := GroupRegistryVersions(images, "acme-app")
	if len(versions) != 2 {
		t.Fatalf("GroupRegistryVersions() = %#v, want 2 versions", versions)
	}
	if versions[0].Digest != digestA || versions[1].Digest != digestB {
		t.Fatalf("GroupRegistryVersions() order = (%s, %s), want (%s, %s)", versions[0].Digest, versions[1].Digest, digestA, digestB)
	}
	if strings.Join(versions[0].Tags, ",") != "build-2,development" {
		t.Fatalf("GroupRegistryVersions() tags = %#v, want build-2 and development", versions[0].Tags)
	}
	if !versions[0].PushedAt.Equal(now) {
		t.Fatalf("GroupRegistryVersions() PushedAt = %v, want %v", versions[0].PushedAt, now)
	}
}

func TestGroupRegistryVersionsOrdersNewestFirstWithStableTieBreak(t *testing.T) {
	now := time.Now()
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	images := []RegistryImage{
		testRegistryImage("acme-app", "b-tag", digestB, now),
		testRegistryImage("acme-app", "a-tag", digestA, now),
	}
	versions := GroupRegistryVersions(images, "acme-app")
	if len(versions) != 2 || versions[0].Digest != digestA || versions[1].Digest != digestB {
		t.Fatalf("GroupRegistryVersions() = %#v, want digest tie-break ascending", versions)
	}
}

func TestDigestForTag(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	versions := []RegistryVersion{
		{Digest: digestA, Tags: []string{"build-1", "development"}},
	}
	if got := DigestForTag(versions, "development"); got != digestA {
		t.Fatalf("DigestForTag(development) = %q, want %q", got, digestA)
	}
	if got := DigestForTag(versions, "missing"); got != "" {
		t.Fatalf("DigestForTag(missing) = %q, want empty", got)
	}
}

func TestServiceVersionsState(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	versions := ServiceVersions{Repository: "acme-app", DeploymentTag: "development", TaggedDigest: digestA, RunningDigest: digestB}
	if !versions.Eligible() {
		t.Fatal("ServiceVersions.Eligible() = false, want true")
	}
	if versions.DeployedDigest() != digestB {
		t.Fatalf("ServiceVersions.DeployedDigest() = %q, want running digest", versions.DeployedDigest())
	}
	if !versions.RestartPending() {
		t.Fatal("ServiceVersions.RestartPending() = false, want true")
	}
	versions.RunningDigest = ""
	if versions.DeployedDigest() != digestA {
		t.Fatalf("ServiceVersions.DeployedDigest() = %q, want tagged digest fallback", versions.DeployedDigest())
	}
	if versions.RestartPending() {
		t.Fatal("ServiceVersions.RestartPending() = true, want false without a running digest")
	}
	if (ServiceVersions{}).Eligible() {
		t.Fatal("ServiceVersions.Eligible() = true for empty state, want false")
	}
}

func TestGroupApplicationVersionTagsReportsServicesAndSkips(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	now := time.Now()
	images := []RegistryImage{
		testRegistryImage("acme-app", "build-2", digestA, now),
		testRegistryImage("acme-app", "development", digestA, now.Add(-time.Hour)),
		testRegistryImage("acme-worker", "development", digestB, now.Add(-2*time.Hour)),
	}
	targets := []ServiceVersionTarget{
		{ServiceName: "frontend", Repository: "acme-app", DeploymentTag: "development"},
		{ServiceName: "worker", Repository: "acme-worker", DeploymentTag: "development"},
	}

	tags := GroupApplicationVersionTags(images, targets)
	if len(tags) != 2 {
		t.Fatalf("GroupApplicationVersionTags() = %#v, want 2 tags", tags)
	}
	if tags[0].Name != "build-2" || tags[1].Name != "development" {
		t.Fatalf("GroupApplicationVersionTags() order = (%q, %q), want (build-2, development)", tags[0].Name, tags[1].Name)
	}
	if strings.Join(tags[0].Services, ",") != "frontend" || strings.Join(tags[0].Missing, ",") != "worker" {
		t.Fatalf("GroupApplicationVersionTags() build-2 = %#v, want frontend and worker skipped", tags[0])
	}
	if strings.Join(tags[1].Services, ",") != "frontend,worker" || len(tags[1].Missing) != 0 {
		t.Fatalf("GroupApplicationVersionTags() development = %#v, want both services", tags[1])
	}
}
