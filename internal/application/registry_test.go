package application

import (
	"testing"
	"time"
)

func TestValidateRegistryKeepCount(t *testing.T) {
	for _, keep := range []int{0, 1, 3, 5, 100} {
		if got, err := ValidateRegistryKeepCount(keep); err != nil || got != keep {
			t.Fatalf("ValidateRegistryKeepCount(%d) = (%d, %v), want (%d, nil)", keep, got, err, keep)
		}
	}
	for _, keep := range []int{-1, 101, 1000} {
		if _, err := ValidateRegistryKeepCount(keep); err == nil {
			t.Fatalf("ValidateRegistryKeepCount(%d) error = nil, want invalid", keep)
		}
	}
}

func TestValidateRegistryRepository(t *testing.T) {
	for _, repository := range []string{"acme-app", "status-page/web", "a.b_c-d/e"} {
		if got, err := ValidateRegistryRepository(repository); err != nil || got != repository {
			t.Fatalf("ValidateRegistryRepository(%q) = (%q, %v), want (%q, nil)", repository, got, err, repository)
		}
	}
	for _, repository := range []string{"", "BAD", "has space", "../escape", "a//b", "-bad", "repo/", "/repo", "UPPER/case"} {
		if _, err := ValidateRegistryRepository(repository); err == nil {
			t.Fatalf("ValidateRegistryRepository(%q) error = nil, want invalid", repository)
		}
	}
}

func TestParseDeployedRegistryReference(t *testing.T) {
	repository, tag, ok := ParseDeployedRegistryReference("localhost:5000/acme-app:abc123")
	if !ok || repository != "acme-app" || tag != "abc123" {
		t.Fatalf("ParseDeployedRegistryReference() = (%q, %q, %v), want (acme-app, abc123, true)", repository, tag, ok)
	}
	for _, image := range []string{"ghcr.io/example/web:v1", "localhost:5000/acme-app", "localhost:5000/BAD:tag", ""} {
		if _, _, ok := ParseDeployedRegistryReference(image); ok {
			t.Fatalf("ParseDeployedRegistryReference(%q) = true, want false", image)
		}
	}
}

func registryTestImage(tag string, pushedAt time.Time) RegistryImage {
	return RegistryImage{Repository: "acme-app", Tag: tag, Digest: "sha256:aaaa", PushedAt: pushedAt}
}

func TestPlanRegistryPurgeKeepsNewest(t *testing.T) {
	now := time.Now()
	images := []RegistryImage{
		registryTestImage("oldest", now.Add(-4*time.Hour)),
		registryTestImage("middle", now.Add(-2*time.Hour)),
		registryTestImage("newer", now.Add(-1*time.Hour)),
		registryTestImage("newest", now),
	}
	kept, purge, err := PlanRegistryPurge(images, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 3 || len(purge) != 1 {
		t.Fatalf("PlanRegistryPurge() kept=%d purge=%d, want 3 and 1", len(kept), len(purge))
	}
	if purge[0].Tag != "oldest" {
		t.Fatalf("PlanRegistryPurge() purge = %q, want oldest", purge[0].Tag)
	}
}

func TestPlanRegistryPurgeProtectsDeployed(t *testing.T) {
	now := time.Now()
	images := []RegistryImage{
		registryTestImage("oldest", now.Add(-4*time.Hour)),
		registryTestImage("middle", now.Add(-2*time.Hour)),
		registryTestImage("newest", now),
	}
	protected := map[string]bool{RegistryImageKey("acme-app", "oldest"): true}
	kept, purge, err := PlanRegistryPurge(images, 1, protected)
	if err != nil {
		t.Fatal(err)
	}
	keptTags := map[string]bool{}
	for _, image := range kept {
		keptTags[image.Tag] = true
	}
	if !keptTags["oldest"] || !keptTags["newest"] {
		t.Fatalf("PlanRegistryPurge() kept = %v, want oldest (deployed) and newest", keptTags)
	}
	if len(purge) != 1 || purge[0].Tag != "middle" {
		t.Fatalf("PlanRegistryPurge() purge = %#v, want middle only", purge)
	}
}

func TestPlanRegistryPurgeUnlimited(t *testing.T) {
	images := []RegistryImage{registryTestImage("a", time.Now()), registryTestImage("b", time.Now())}
	kept, purge, err := PlanRegistryPurge(images, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 || len(purge) != 0 {
		t.Fatalf("PlanRegistryPurge(unlimited) kept=%d purge=%d, want 2 and 0", len(kept), len(purge))
	}
}

func TestEffectiveRegistryKeep(t *testing.T) {
	policies := map[string]int{"acme-app": 3}
	if got := EffectiveRegistryKeep(5, policies, "acme-app"); got != 3 {
		t.Fatalf("EffectiveRegistryKeep() = %d, want 3", got)
	}
	if got := EffectiveRegistryKeep(5, policies, "other"); got != 5 {
		t.Fatalf("EffectiveRegistryKeep() = %d, want 5", got)
	}
	if got := EffectiveRegistryKeep(5, map[string]int{"acme-app": 0}, "acme-app"); got != 0 {
		t.Fatalf("EffectiveRegistryKeep() = %d, want 0 unlimited", got)
	}
}
