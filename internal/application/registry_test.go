package application

import (
	"testing"
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
