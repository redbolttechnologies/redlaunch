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
	"time"
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

// RegistryContent is one tagged image or retained untagged manifest revision
// in the managed local registry. Tag is empty for an untagged revision.
// Digest identifies the manifest. PushedAt is the tag or revision link
// modification time; it may be zero when the time could not be read.
type RegistryContent struct {
	Repository string
	Tag        string
	Digest     string
	PushedAt   time.Time
}

// ListRegistryContents returns tagged images and retained untagged manifest
// revisions previously pushed to the managed local image registry container. It reads through the Docker daemon, so it
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

// DeleteRegistryTag removes one tag directory from the managed registry
// container through the Docker daemon. Only the tag link tree is removed;
// content-addressable blobs remain until the operator runs
// `registry garbage-collect`. Repository and tag are strictly validated so a
// user-controlled value cannot escape the repositories tree. Arguments are
// passed exec-style without a shell.
func (r CommandRunner) DeleteRegistryTag(ctx context.Context, containerName, repository, tag string) error {
	ctx = normalizeContext(ctx)
	containerName = strings.TrimSpace(containerName)
	if containerName == "" {
		return errors.New("registry container name is required")
	}
	repository = strings.TrimSpace(repository)
	if repository == "" || len(repository) > 255 {
		return errors.New("registry repository is invalid")
	}
	parts := strings.Split(repository, "/")
	for _, part := range parts {
		if !validRegistryPathComponent(part) {
			return errors.New("registry repository is invalid")
		}
	}
	tag = strings.TrimSpace(tag)
	if !validRegistryTag(tag) {
		return errors.New("registry tag is invalid")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	containerPath := registryRepositoriesPath + "/" + repository + "/_manifests/tags/" + tag
	remove := exec.CommandContext(ctx, binary, "exec", containerName, "rm", "-rf", "--", containerPath)
	remove.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(remove, "delete registry tag", ""); err != nil {
		return err
	}
	return nil
}

// ErrRegistryImageAbsent reports that a registry repository does not contain
// the requested manifest, so a tag cannot be assigned to it.
var ErrRegistryImageAbsent = errors.New("registry repository does not contain the requested image")

// registryRevisionPath returns the manifest revision link for one digest
// inside a repository. The digest must already be validated.
func registryRevisionPath(repository, digest string) string {
	_, encoded, _ := strings.Cut(digest, ":")
	return registryRepositoriesPath + "/" + repository + "/_manifests/revisions/sha256/" + encoded + "/link"
}

// registryTagLinkPath returns the tag link file inside a repository. The
// repository and tag must already be validated.
func registryTagLinkPath(repository, tag string) string {
	return registryRepositoriesPath + "/" + repository + "/_manifests/tags/" + tag + "/current/link"
}

// MoveRegistryTag assigns one registry tag to a manifest digest, the storage
// operation behind a version switch. It returns the digest the tag was
// assigned to before the move, or an empty string when the tag did not exist
// yet. The target digest must be a manifest previously pushed to the same
// repository; the new link is written through a temporary file and an atomic
// rename so a failed write cannot leave the tag half-moved. Repository, tag,
// and digest are strictly validated and arguments are passed exec-style
// without a shell.
func (r CommandRunner) MoveRegistryTag(ctx context.Context, containerName, repository, tag, digest string) (string, error) {
	ctx = normalizeContext(ctx)
	containerName = strings.TrimSpace(containerName)
	if containerName == "" {
		return "", errors.New("registry container name is required")
	}
	repository = strings.TrimSpace(repository)
	if repository == "" || len(repository) > 255 {
		return "", errors.New("registry repository is invalid")
	}
	for _, part := range strings.Split(repository, "/") {
		if !validRegistryPathComponent(part) {
			return "", errors.New("registry repository is invalid")
		}
	}
	tag = strings.TrimSpace(tag)
	if !validRegistryTag(tag) {
		return "", errors.New("registry tag is invalid")
	}
	digest = strings.TrimSpace(digest)
	if !validRegistryDigest(digest) {
		return "", errors.New("registry digest is invalid")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}

	revisionPath := registryRevisionPath(repository, digest)
	revision := exec.CommandContext(ctx, binary, "exec", containerName, "cat", "--", revisionPath)
	revision.Env = composeProcessEnvironment(os.Environ(), nil)
	revisionOutput, revisionErr := runStructuredCommand(revision, "read registry manifest revision", "")
	if revisionErr != nil || strings.TrimSpace(string(revisionOutput)) != digest {
		return "", errors.Join(ErrRegistryImageAbsent, revisionErr)
	}

	linkPath := registryTagLinkPath(repository, tag)
	previous := ""
	previousCommand := exec.CommandContext(ctx, binary, "exec", containerName, "cat", "--", linkPath)
	previousCommand.Env = composeProcessEnvironment(os.Environ(), nil)
	if previousOutput, previousErr := runStructuredCommand(previousCommand, "read registry tag link", ""); previousErr == nil {
		previous = strings.TrimSpace(string(previousOutput))
		if !validRegistryDigest(previous) {
			previous = ""
		}
	}

	tagDirectory := registryRepositoriesPath + "/" + repository + "/_manifests/tags/" + tag + "/current"
	makeDirectory := exec.CommandContext(ctx, binary, "exec", containerName, "mkdir", "-p", "--", tagDirectory)
	makeDirectory.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(makeDirectory, "create registry tag directory", ""); err != nil {
		return previous, err
	}

	staging, err := os.CreateTemp("", "redlaunch-registry-link-")
	if err != nil {
		return previous, fmt.Errorf("create registry link staging file: %w", err)
	}
	stagingPath := staging.Name()
	defer os.Remove(stagingPath)
	if _, err := staging.WriteString(digest); err != nil {
		staging.Close()
		return previous, fmt.Errorf("write registry link staging file: %w", err)
	}
	if err := staging.Close(); err != nil {
		return previous, fmt.Errorf("close registry link staging file: %w", err)
	}

	stagedLink := linkPath + ".new"
	copyCommand := exec.CommandContext(ctx, binary, "cp", stagingPath, containerName+":"+stagedLink)
	copyCommand.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(copyCommand, "stage registry tag link", ""); err != nil {
		return previous, err
	}
	rename := exec.CommandContext(ctx, binary, "exec", containerName, "mv", "-f", "--", stagedLink, linkPath)
	rename.Env = composeProcessEnvironment(os.Environ(), nil)
	if err := runDiagnosticCommand(rename, "assign registry tag", ""); err != nil {
		cleanup := exec.CommandContext(ctx, binary, "exec", containerName, "rm", "-f", "--", stagedLink)
		cleanup.Env = composeProcessEnvironment(os.Environ(), nil)
		_ = runDiagnosticCommand(cleanup, "remove staged registry tag link", "")
		return previous, err
	}
	return previous, nil
}

var errRegistryImageLimit = errors.New("registry image limit reached")

// readRegistryContents walks a copied repositories tree and returns one
// entry per tag link, plus retained revisions without tags. Entries with invalid
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
	revisions := make([]RegistryContent, 0)
	tagged := make(map[string]bool)
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
		revisionDigest := ""
		if !ok {
			repository, revisionDigest, ok = splitRegistryRevisionPath(root, path)
			if !ok || len(revisions) >= maxRegistryImages {
				return nil
			}
		}
		digest, err := readRegistryDigest(path)
		if err != nil {
			return err
		}
		if digest == "" {
			return nil
		}
		content := RegistryContent{
			Repository: repository,
			Tag:        tag,
			Digest:     digest,
			PushedAt:   fileInfo.ModTime(),
		}
		if revisionDigest != "" {
			if digest == revisionDigest {
				revisions = append(revisions, content)
			}
		} else {
			tagged[repository+"\x00"+digest] = true
			contents = append(contents, content)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errRegistryImageLimit) {
		return nil, fmt.Errorf("read copied registry repositories: %w", err)
	}
	for _, revision := range revisions {
		if len(contents) >= maxRegistryImages {
			break
		}
		if !tagged[revision.Repository+"\x00"+revision.Digest] {
			contents = append(contents, revision)
		}
	}
	return contents, nil
}

// splitRegistryRevisionPath accepts only repository manifest revision links.
func splitRegistryRevisionPath(root, path string) (string, string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 6 {
		return "", "", false
	}
	tail := parts[len(parts)-5:]
	if tail[0] != "_manifests" || tail[1] != "revisions" || tail[2] != "sha256" || tail[4] != "link" {
		return "", "", false
	}
	digest := "sha256:" + tail[3]
	if !validRegistryDigest(digest) {
		return "", "", false
	}
	repositoryParts := parts[:len(parts)-5]
	for _, part := range repositoryParts {
		if !validRegistryPathComponent(part) {
			return "", "", false
		}
	}
	repository := strings.Join(repositoryParts, "/")
	if len(repository) > 255 {
		return "", "", false
	}
	return repository, digest, true
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
