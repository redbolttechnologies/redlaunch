package github

import (
	"net/url"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func TestValidateManifestRequiresChecksummedExpectedAssets(t *testing.T) {
	sha := strings.Repeat("a", 64)
	assets := map[string]application.ReleaseAsset{
		"redlaunch-image-linux-amd64.tar.gz": {Name: "redlaunch-image-linux-amd64.tar.gz"},
	}
	checksums := map[string]string{"redlaunch-image-linux-amd64.tar.gz": sha}
	manifest := application.ReleaseManifest{
		Version:            "v1.2.3",
		Commit:             "abc123",
		DeploymentContract: 1,
		Images: map[string]application.ReleaseImage{
			"amd64": {
				Arch:    "amd64",
				Asset:   "redlaunch-image-linux-amd64.tar.gz",
				SHA256:  sha,
				ImageID: "sha256:" + strings.Repeat("b", 64),
				Tag:     "redlaunch:v1.2.3",
			},
		},
	}
	if err := validateManifest(manifest, "v1.2.3", assets, checksums); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.Images["amd64"] = application.ReleaseImage{
		Arch:    "amd64",
		Asset:   "redlaunch-image-linux-amd64.tar.gz",
		SHA256:  strings.Repeat("c", 64),
		ImageID: "sha256:" + strings.Repeat("b", 64),
		Tag:     "redlaunch:v1.2.3",
	}
	if err := validateManifest(manifest, "v1.2.3", assets, checksums); err == nil {
		t.Fatal("manifest with mismatched checksum accepted")
	}
}

func TestValidateManifestRejectsUnsafeAssetNames(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, name := range []string{"", ".", "..", "../image.tar.gz", "nested/image.tar.gz", `nested\image.tar.gz`} {
		assets := map[string]application.ReleaseAsset{name: {Name: name}}
		checksums := map[string]string{name: sha}
		manifest := application.ReleaseManifest{
			Version:            "v1.2.3",
			Commit:             "abc123",
			DeploymentContract: 1,
			Images: map[string]application.ReleaseImage{
				"amd64": {
					Arch:    "amd64",
					Asset:   name,
					SHA256:  sha,
					ImageID: "sha256:" + strings.Repeat("b", 64),
					Tag:     "redlaunch:v1.2.3",
				},
			},
		}
		if err := validateManifest(manifest, "v1.2.3", assets, checksums); err == nil {
			t.Fatalf("unsafe asset name %q accepted", name)
		}
	}
}

func TestReleaseURLValidation(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/example/release",
		"https://api.github.com/repos/example/release",
		"https://objects.githubusercontent.com/file",
	} {
		parsed, err := url.Parse(raw)
		if err != nil || validateURL(parsed) != nil {
			t.Fatalf("validateURL(%q) rejected", raw)
		}
	}
	for _, raw := range []string{
		"http://github.com/file",
		"https://example.com/file",
		"https://user@github.com/file",
	} {
		parsed, err := url.Parse(raw)
		if err == nil && validateURL(parsed) == nil {
			t.Fatalf("validateURL(%q) accepted", raw)
		}
	}
}
