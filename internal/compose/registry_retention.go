package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
)

func validateRegistryVersion(container, repository, digest string) error {
	if strings.TrimSpace(container) == "" {
		return errors.New("registry container name is required")
	}
	if repository == "" || len(repository) > 255 {
		return errors.New("registry repository is invalid")
	}
	for _, part := range strings.Split(repository, "/") {
		if !validRegistryPathComponent(part) {
			return errors.New("registry repository is invalid")
		}
	}
	if !validRegistryDigest(digest) {
		return errors.New("registry digest is invalid")
	}
	return nil
}

// RegistryManifestReferences reads only a manifest blob, never image layers.
// Index children must remain reachable for retained multi-platform images.
func (r CommandRunner) RegistryManifestReferences(ctx context.Context, container, repository, digest string) ([]string, error) {
	if err := validateRegistryVersion(container, repository, digest); err != nil {
		return nil, err
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	encoded := strings.TrimPrefix(digest, "sha256:")
	path := "/var/lib/registry/docker/registry/v2/blobs/sha256/" + encoded[:2] + "/" + encoded + "/data"
	command := exec.CommandContext(normalizeContext(ctx), binary, "exec", container, "cat", "--", path)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := runStructuredCommand(command, "read registry manifest", "")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &manifest); err != nil {
		return nil, errors.New("registry manifest is invalid")
	}
	if manifest.SchemaVersion != 2 {
		return nil, errors.New("registry manifest schema is unsupported")
	}
	refs := make([]string, 0, len(manifest.Manifests))
	for _, child := range manifest.Manifests {
		if !validRegistryDigest(child.Digest) {
			return nil, errors.New("registry child manifest digest is invalid")
		}
		refs = append(refs, child.Digest)
	}
	return refs, nil
}

// DeleteRegistryVersion removes a manifest revision and its current tags.
// Re-reading tags at deletion time avoids deleting tags that moved since the
// retention snapshot. Blobs are deliberately left for offline garbage collection;
// deleting shared layers while the registry accepts pushes is unsafe.
func (r CommandRunner) DeleteRegistryVersion(ctx context.Context, container, repository, digest string) error {
	if err := validateRegistryVersion(container, repository, digest); err != nil {
		return err
	}
	contents, err := r.ListRegistryContents(ctx, container)
	if err != nil {
		return err
	}
	for _, content := range contents {
		if content.Repository == repository && content.Digest == digest && content.Tag != "" {
			if err := r.DeleteRegistryTag(ctx, container, repository, content.Tag); err != nil {
				return err
			}
		}
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	path := strings.TrimSuffix(registryRevisionPath(repository, digest), "/link")
	command := exec.CommandContext(normalizeContext(ctx), binary, "exec", container, "rm", "-rf", "--", path)
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	return runDiagnosticCommand(command, "delete registry manifest revision", "")
}
