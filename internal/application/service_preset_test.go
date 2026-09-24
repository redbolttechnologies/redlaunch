package application

import (
	"strings"
	"testing"
)

func TestFindServicePresetNormalizesLookup(t *testing.T) {
	preset, ok := FindServicePreset("  PGADMIN ")
	if !ok {
		t.Fatal("FindServicePreset(padded uppercase) = false, want true")
	}
	if preset.Slug != "pgadmin" {
		t.Fatalf("FindServicePreset slug = %q, want %q", preset.Slug, "pgadmin")
	}
	if _, ok := FindServicePreset("does-not-exist"); ok {
		t.Fatal("FindServicePreset(unknown) = true, want false")
	}
	if _, ok := FindServicePreset(""); ok {
		t.Fatal("FindServicePreset(empty) = true, want false")
	}
}

func TestServicePresetCatalogIsValid(t *testing.T) {
	presets := AllServicePresets()
	if len(presets) < 5 {
		t.Fatalf("AllServicePresets() has %d entries, want at least 5", len(presets))
	}
	seen := map[string]struct{}{}
	categories := map[string]struct{}{}
	for _, category := range ServicePresetCategories() {
		categories[category.Slug] = struct{}{}
	}
	for _, preset := range presets {
		if preset.Slug == "" || preset.Name == "" || preset.Description == "" {
			t.Fatalf("preset %+v is missing slug, name, or description", preset)
		}
		if preset.Slug != strings.ToLower(strings.TrimSpace(preset.Slug)) {
			t.Fatalf("preset slug %q is not normalized", preset.Slug)
		}
		if _, duplicate := seen[preset.Slug]; duplicate {
			t.Fatalf("duplicate preset slug %q", preset.Slug)
		}
		seen[preset.Slug] = struct{}{}
		if _, ok := categories[preset.Category]; !ok {
			t.Fatalf("preset %q has unknown category %q", preset.Slug, preset.Category)
		}
		if _, err := ValidateImageName(preset.Image); err != nil {
			t.Fatalf("preset %q has invalid image %q: %v", preset.Slug, preset.Image, err)
		}
		if preset.DefaultServiceName == "" {
			t.Fatalf("preset %q is missing a default service name", preset.Slug)
		}
		if _, err := ValidateServiceName(preset.DefaultServiceName); err != nil {
			t.Fatalf("preset %q has invalid service name %q: %v", preset.Slug, preset.DefaultServiceName, err)
		}
		if preset.RestartPolicy == "" {
			t.Fatalf("preset %q is missing a restart policy", preset.Slug)
		}
		for _, port := range preset.PortMappings {
			if strings.TrimSpace(port.HostPort) == "" || strings.TrimSpace(port.ContainerPort) == "" {
				t.Fatalf("preset %q has an incomplete port mapping %+v", preset.Slug, port)
			}
		}
		for _, volume := range preset.VolumeMappings {
			if strings.TrimSpace(volume.Source) == "" || strings.TrimSpace(volume.Target) == "" {
				t.Fatalf("preset %q has an incomplete volume mapping %+v", preset.Slug, volume)
			}
		}
		if strings.Contains(preset.EnvNote, "\n") {
			t.Fatalf("preset %q env note must stay single-line", preset.Slug)
		}
	}
}

func TestServicePresetCategoryLabelFallsBack(t *testing.T) {
	if got := ServicePresetCategoryLabel(ServicePresetCategoryDatabase); got != "Database" {
		t.Fatalf("category label = %q, want %q", got, "Database")
	}
	if got := ServicePresetCategoryLabel("unknown"); got != "unknown" {
		t.Fatalf("unknown category label = %q, want slug", got)
	}
}
