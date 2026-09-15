package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrRegistryUnavailable reports that the managed local image registry
// container cannot be reached through the Docker daemon, either because the
// registry is not installed or because Docker itself is unreachable.
var ErrRegistryUnavailable = errors.New("local image registry is unavailable")

// registryRepositoriesPath is the repositories tree inside the managed
// registry container. It holds one directory per pushed repository with a
// _manifests/tags/<tag>/current/link file per pushed tag. Only these small
// link files are read; the content-addressable blob store lives in a
// separate tree and is never transferred.
const registryRepositoriesPath = "/var/lib/registry/docker/registry/v2/repositories"

// maxRegistryImages bounds the Registry page. Repository keys can push to
// any repository path, so the number of stored tags is capped before
// rendering.
const maxRegistryImages = 1000

// RegistryContent is one tag previously pushed to the managed local image
// registry. Digest is the manifest digest the tag currently points at.
type RegistryContent struct {
	Repository string
	Tag        string
	Digest     string
}

// ListRegistryContents returns the tags previously pushed to the managed
// local image registry container. It reads through the Docker daemon, so it
// works whether Redlaunch runs directly on the host or inside its own
// container, where the registry's loopback-bound port is unreachable.
//
// A registry that has never received a push has no repositories tree yet;
// that reports an empty list. A missing registry container reports
// ErrRegistryUnavailable.
func (r CommandRunner) ListRegistryContents(ctx context.Context, containerName string) ([]RegistryContent, error) {
	ctx = normalizeContext(ctx)
	containerName = strings.TrimSpace(containerName)
	if containerName == "" {
		return nil, errors.New("registry container name is required")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	inspect := exec.CommandContext(ctx, binary, "inspect", containerName)
	inspect.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(inspect, "inspect registry container", ""); err != nil {
		return nil, errors.Join(ErrRegistryUnavailable, err)
	}

	staging, err := os.MkdirTemp("", "redlaunch-registry-")
	if err != nil {
		return nil, fmt.Errorf("create registry staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	copyCommand := exec.CommandContext(ctx, binary, "cp", containerName+":"+registryRepositoriesPath, staging)
	copyCommand.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(copyCommand, "copy registry repositories", ""); err != nil {
		// The repositories tree only appears after the first push. The
		// Docker CLI reports that case as a missing file while every other
		// failure keeps its diagnostic error.
		if isRegistryPathNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return readRegistryContents(filepath.Join(staging, "repositories"))
}

// isRegistryPathNotFound reports the Docker CLI's missing-source message for
// docker cp. The CLI message is English-only, and any other failure keeps
// its full diagnostic error.
func isRegistryPathNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Could not find the file")
}

var errRegistryImageLimit = errors.New("registry image limit reached")

// readRegistryContents walks a copied repositories tree and returns one
// entry per _manifests/tags/<tag>/current/link file. Entries with invalid
// repository, tag, or digest values are skipped so one unexpected file
// cannot break the whole listing.
func readRegistryContents(root string) ([]RegistryContent, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect copied registry repositories: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("copied registry repositories is not a directory")
	}

	contents := make([]RegistryContent, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(contents) >= maxRegistryImages {
			return errRegistryImageLimit
		}
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return nil
		}
		repository, tag, ok := splitRegistryLinkPath(root, path)
		if !ok {
			return nil
		}
		digest, err := readRegistryDigest(path)
		if err != nil {
			return err
		}
		if digest == "" {
			return nil
		}
		contents = append(contents, RegistryContent{
			Repository: repository,
			Tag:        tag,
			Digest:     digest,
		})
		return nil
	})
	if errors.Is(err, errRegistryImageLimit) {
		return contents, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read copied registry repositories: %w", err)
	}
	return contents, nil
}

// splitRegistryLinkPath extracts the repository and tag from a
// <repository>/_manifests/tags/<tag>/current/link path below root. It
// reports false for any other layout.
func splitRegistryLinkPath(root, path string) (string, string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 6 {
		return "", "", false
	}
	tail := parts[len(parts)-5:]
	if tail[0] != "_manifests" || tail[1] != "tags" || tail[3] != "current" || tail[4] != "link" {
		return "", "", false
	}
	tag := tail[2]
	if !validRegistryTag(tag) {
		return "", "", false
	}
	repositoryParts := parts[:len(parts)-5]
	for _, part := range repositoryParts {
		if !validRegistryPathComponent(part) {
			return "", "", false
		}
	}
	repository := strings.Join(repositoryParts, "/")
	if repository == "" || len(repository) > 255 {
		return "", "", false
	}
	return repository, tag, true
}

// readRegistryDigest reads a tag link file, which contains a manifest
// digest such as sha256:<hex>. It reports an empty digest when the file
// cannot be read or does not hold a valid digest.
func readRegistryDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 256))
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(contents))
	if !validRegistryDigest(digest) {
		return "", nil
	}
	return digest, nil
}

// validRegistryPathComponent accepts one lowercase repository path
// component in the Docker distribution form.
func validRegistryPathComponent(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		lowerAlphanumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if index == 0 || index == len(value)-1 {
			if !lowerAlphanumeric {
				return false
			}
			continue
		}
		if !lowerAlphanumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// validRegistryTag accepts a Docker distribution tag.
func validRegistryTag(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		word := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_'
		if index == 0 {
			if !word {
				return false
			}
			continue
		}
		if !word && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

// validRegistryDigest accepts a manifest digest reference.
func validRegistryDigest(value string) bool {
	algorithm, encoded, ok := strings.Cut(value, ":")
	if !ok || algorithm != "sha256" || len(encoded) != 64 {
		return false
	}
	for _, character := range encoded {
		hexDigit := character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F'
		if !hexDigit {
			return false
		}
	}
	return true
}
