package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

const (
	updateArchiveMaxEntries      = 10000
	updateArchiveMaxManifest     = 1 * 1024 * 1024
	updateArchiveMaxConfig       = 16 * 1024 * 1024
	updateArchiveMaxUncompressed = int64(2 * 1024 * 1024 * 1024)
)

var errArchiveTooLarge = errors.New("image archive is too large")

// boundedReader enforces a limit on the total decompressed stream, including
// tar headers, PAX metadata, padding, and trailing data. Counting only entry
// payload sizes would let metadata or a lying header bypass the bound.
type boundedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errArchiveTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type savedImageManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

type savedImageConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// validateImageArchive checks the narrow Docker save format emitted by the
// Redlaunch release workflow before the archive is imported. It never extracts
// entries onto the host filesystem.
func validateImageArchive(ctx context.Context, archivePath, expectedTag, expectedVersion, expectedCommit string, expectedContract int) error {
	if strings.TrimSpace(archivePath) == "" || strings.TrimSpace(expectedTag) == "" {
		return errors.New("image archive validation is not configured")
	}
	sizes, digests, manifestBytes, indexBytes, repositoriesBytes, err := readArchiveIndex(ctx, archivePath)
	if err != nil {
		return err
	}
	if err := validateArchiveReferences(indexBytes, repositoriesBytes, expectedTag); err != nil {
		return err
	}
	var manifests []savedImageManifest
	if err := json.Unmarshal(manifestBytes, &manifests); err != nil || len(manifests) != 1 {
		return errors.New("image archive manifest is invalid")
	}
	manifest := manifests[0]
	if manifest.Config == "" || len(manifest.RepoTags) != 1 || manifest.RepoTags[0] != expectedTag {
		return errors.New("image archive contains an unexpected image tag")
	}
	if !safeArchivePath(manifest.Config) || !archiveEntryExists(sizes, manifest.Config) {
		return errors.New("image archive config is missing or unsafe")
	}
	if len(manifest.Layers) == 0 {
		return errors.New("image archive has no layers")
	}
	for _, layer := range manifest.Layers {
		if !safeArchivePath(layer) || !archiveEntryExists(sizes, layer) {
			return errors.New("image archive layer is missing or unsafe")
		}
	}
	configBytes, err := readArchiveEntry(ctx, archivePath, manifest.Config, updateArchiveMaxConfig)
	if err != nil {
		return err
	}
	var config savedImageConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return errors.New("image archive config is invalid")
	}
	if config.OS != "linux" {
		return errors.New("image archive is not a Linux image")
	}
	if config.Config.Labels["org.opencontainers.image.version"] != expectedVersion {
		return errors.New("image archive version label does not match the release")
	}
	if expectedCommit != "" && config.Config.Labels["org.opencontainers.image.revision"] != expectedCommit {
		return errors.New("image archive revision label does not match the release")
	}
	if expectedContract > 0 && config.Config.Labels["io.redlaunch.deployment-contract"] != fmt.Sprintf("%d", expectedContract) {
		return errors.New("image archive deployment contract does not match")
	}
	if config.Architecture == "" {
		return errors.New("image archive architecture is missing")
	}
	if len(indexBytes) > 0 {
		if err := validateArchiveGraph(ctx, archivePath, sizes, digests, indexBytes, manifest.Config, manifest.Layers); err != nil {
			return err
		}
	}
	return nil
}

// ociDescriptor is the subset of an OCI descriptor needed to walk a saved
// image graph. Every referenced blob is checked against the tar entries and
// the descriptor's declared size.
type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

type ociDocument struct {
	Config    *ociDescriptor  `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

type ociLeaf struct {
	configDigest string
	layerDigests []string
	attestation  bool
}

func archiveEntryExists(sizes map[string]int64, name string) bool {
	_, ok := sizes[name]
	return ok
}

// validateArchiveGraph verifies that index.json describes the same single
// image as manifest.json and that every referenced blob exists with its
// declared size. index.json is what containerd-backed Docker daemons consume,
// so accepting it without validating its graph would bypass the manifest
// checks.
func validateArchiveGraph(ctx context.Context, archivePath string, sizes map[string]int64, digests map[string]string, indexBytes []byte, configPath string, layerPaths []string) error {
	var index ociDocument
	if err := json.Unmarshal(indexBytes, &index); err != nil || len(index.Manifests) != 1 {
		return errors.New("image archive index is invalid")
	}
	expectedConfig, ok := archiveBlobPath(configPath)
	if !ok {
		return errors.New("image archive config is not a digest reference")
	}
	expectedLayers := make([]string, 0, len(layerPaths))
	for _, layer := range layerPaths {
		blob, ok := archiveBlobPath(layer)
		if !ok {
			return errors.New("image archive layer is not a digest reference")
		}
		expectedLayers = append(expectedLayers, blob)
	}

	seen := make(map[string]bool)
	var leaves []ociLeaf
	var walk func(ociDescriptor, int) error
	walk = func(descriptor ociDescriptor, depth int) error {
		if depth > 4 {
			return errors.New("image archive index is nested too deeply")
		}
		if len(seen) >= 128 {
			return errors.New("image archive index has too many descriptors")
		}
		blob, ok := archiveBlobPath(descriptor.Digest)
		if !ok || seen[descriptor.Digest] {
			return errors.New("image archive index contains an invalid descriptor")
		}
		seen[descriptor.Digest] = true
		size, ok := sizes[blob]
		if !ok {
			return errors.New("image archive index references a missing blob")
		}
		if descriptor.Size <= 0 || size != descriptor.Size {
			return errors.New("image archive index blob size does not match")
		}
		if digests[blob] != strings.TrimPrefix(descriptor.Digest, "sha256:") {
			return errors.New("image archive index blob digest does not match")
		}
		contents, err := readArchiveEntry(ctx, archivePath, blob, updateArchiveMaxManifest)
		if err != nil {
			return err
		}
		var document ociDocument
		if err := json.Unmarshal(contents, &document); err != nil {
			return errors.New("image archive index descriptor is invalid")
		}
		switch {
		case len(document.Manifests) > 0:
			if document.Config != nil || len(document.Layers) > 0 {
				return errors.New("image archive index descriptor is ambiguous")
			}
			for _, child := range document.Manifests {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case document.Config != nil:
			configBlob, ok := archiveBlobPath(document.Config.Digest)
			if !ok {
				return errors.New("image archive index config is invalid")
			}
			configSize, ok := sizes[configBlob]
			if !ok || document.Config.Size <= 0 || configSize != document.Config.Size {
				return errors.New("image archive index config is missing or has the wrong size")
			}
			if digests[configBlob] != strings.TrimPrefix(document.Config.Digest, "sha256:") {
				return errors.New("image archive index config digest does not match")
			}
			layerDigests := make([]string, 0, len(document.Layers))
			for _, layer := range document.Layers {
				layerBlob, ok := archiveBlobPath(layer.Digest)
				if !ok {
					return errors.New("image archive index layer is invalid")
				}
				layerSize, ok := sizes[layerBlob]
				if !ok || layer.Size <= 0 || layerSize != layer.Size {
					return errors.New("image archive index layer is missing or has the wrong size")
				}
				if digests[layerBlob] != strings.TrimPrefix(layer.Digest, "sha256:") {
					return errors.New("image archive index layer digest does not match")
				}
				layerDigests = append(layerDigests, layerBlob)
			}
			leaves = append(leaves, ociLeaf{
				configDigest: configBlob,
				layerDigests: layerDigests,
				attestation:  descriptor.Annotations["vnd.docker.reference.type"] == "attestation-manifest",
			})
		default:
			return errors.New("image archive index contains an unsupported descriptor")
		}
		return nil
	}
	for _, descriptor := range index.Manifests {
		if err := walk(descriptor, 0); err != nil {
			return err
		}
	}
	matched := false
	for _, leaf := range leaves {
		if leaf.configDigest == expectedConfig && slices.Equal(leaf.layerDigests, expectedLayers) {
			if matched {
				return errors.New("image archive index describes the saved image more than once")
			}
			matched = true
			continue
		}
		if !leaf.attestation {
			return errors.New("image archive index describes an unexpected image")
		}
	}
	if !matched {
		return errors.New("image archive index does not describe the saved image")
	}
	return nil
}

// archiveBlobPath converts a digest reference or a Docker save entry name to
// the canonical OCI blob path used by index.json.
func archiveBlobPath(name string) (string, bool) {
	if digest, ok := strings.CutPrefix(name, "sha256:"); ok {
		if validSHA256Hex(digest) {
			return "blobs/sha256/" + digest, true
		}
		return "", false
	}
	if strings.HasPrefix(name, "blobs/sha256/") {
		digest := strings.TrimPrefix(name, "blobs/sha256/")
		if validSHA256Hex(digest) {
			return name, true
		}
		return "", false
	}
	for _, suffix := range []string{".json", "/json", "/layer.tar"} {
		if strings.HasSuffix(name, suffix) {
			if digest := strings.TrimSuffix(name, suffix); validSHA256Hex(digest) {
				return "blobs/sha256/" + digest, true
			}
		}
	}
	return "", false
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func readArchiveIndex(ctx context.Context, archivePath string) (map[string]int64, map[string]string, []byte, []byte, []byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(contextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, nil, nil, nil, nil, errors.New("image archive is not a gzip archive")
	}
	defer gzipReader.Close()
	bounded := &boundedReader{reader: gzipReader, remaining: updateArchiveMaxUncompressed}
	reader := tar.NewReader(bounded)
	sizes := make(map[string]int64)
	digests := make(map[string]string)
	var manifestBytes []byte
	var indexBytes []byte
	var repositoriesBytes []byte
	var total int64
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, nil, errors.New("image archive validation timed out")
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, errArchiveTooLarge) {
				return nil, nil, nil, nil, nil, errArchiveTooLarge
			}
			return nil, nil, nil, nil, nil, errors.New("image archive is invalid")
		}
		entries++
		if entries > updateArchiveMaxEntries {
			return nil, nil, nil, nil, nil, errors.New("image archive has too many entries")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "" || !safeArchivePath(name) {
			return nil, nil, nil, nil, nil, errors.New("image archive contains an unsafe path")
		}
		// Only regular files and directories are part of the supported save
		// format. Symlinks, hard links, devices, FIFOs, and PAX headers could
		// otherwise redirect validation or introduce unvalidated content.
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
		default:
			return nil, nil, nil, nil, nil, errors.New("image archive contains an unsupported entry")
		}
		if header.Linkname != "" {
			return nil, nil, nil, nil, nil, errors.New("image archive contains an unsupported link")
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if _, exists := sizes[name]; exists {
			return nil, nil, nil, nil, nil, errors.New("image archive contains duplicate entries")
		}
		sizes[name] = header.Size
		// Bound the actual decompressed stream, not just declared sizes. Sizes
		// are validated first, then every entry body is consumed with a shared
		// limit so a lying header cannot bypass the bound.
		if header.Size < 0 || header.Size > updateArchiveMaxUncompressed {
			return nil, nil, nil, nil, nil, errors.New("image archive is too large")
		}
		remaining := updateArchiveMaxUncompressed - total
		if header.Size > remaining {
			return nil, nil, nil, nil, nil, errors.New("image archive is too large")
		}
		hasher := sha256.New()
		switch name {
		case "manifest.json":
			if header.Size > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive manifest is too large")
			}
			manifestBytes, err = io.ReadAll(io.LimitReader(io.TeeReader(reader, hasher), updateArchiveMaxManifest+1))
			if err != nil || len(manifestBytes) > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive manifest could not be read")
			}
			total += int64(len(manifestBytes))
		case "index.json":
			if header.Size > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive index is too large")
			}
			indexBytes, err = io.ReadAll(io.LimitReader(io.TeeReader(reader, hasher), updateArchiveMaxManifest+1))
			if err != nil || len(indexBytes) > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive index could not be read")
			}
			total += int64(len(indexBytes))
		case "repositories":
			if header.Size > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive repositories file is too large")
			}
			repositoriesBytes, err = io.ReadAll(io.LimitReader(io.TeeReader(reader, hasher), updateArchiveMaxManifest+1))
			if err != nil || len(repositoriesBytes) > updateArchiveMaxManifest {
				return nil, nil, nil, nil, nil, errors.New("image archive repositories file could not be read")
			}
			total += int64(len(repositoriesBytes))
		default:
			discarded, err := io.CopyN(io.MultiWriter(io.Discard, hasher), reader, header.Size+1)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, nil, nil, nil, nil, errors.New("image archive is invalid")
			}
			if discarded > header.Size {
				return nil, nil, nil, nil, nil, errors.New("image archive entry exceeds declared size")
			}
			total += discarded
		}
		digests[name] = hex.EncodeToString(hasher.Sum(nil))
		if total > updateArchiveMaxUncompressed {
			return nil, nil, nil, nil, nil, errors.New("image archive is too large")
		}
	}
	if len(manifestBytes) == 0 {
		return nil, nil, nil, nil, nil, errors.New("image archive manifest is missing")
	}
	// Reject trailing decompressed data after the tar stream so Docker cannot
	// process content this validator did not inspect.
	var extra [1]byte
	if n, readErr := bounded.Read(extra[:]); n > 0 {
		return nil, nil, nil, nil, nil, errors.New("image archive contains trailing data")
	} else if readErr != nil && !errors.Is(readErr, io.EOF) {
		if errors.Is(readErr, errArchiveTooLarge) {
			return nil, nil, nil, nil, nil, errArchiveTooLarge
		}
		return nil, nil, nil, nil, nil, errors.New("image archive is invalid")
	}
	return sizes, digests, manifestBytes, indexBytes, repositoriesBytes, nil
}

func validateArchiveReferences(indexBytes, repositoriesBytes []byte, expectedTag string) error {
	if len(indexBytes) > 0 {
		var index struct {
			Manifests []struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"manifests"`
		}
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return errors.New("image archive index is invalid")
		}
		if len(index.Manifests) != 1 {
			return errors.New("image archive index must describe exactly one image")
		}
		tagName := expectedTag
		if separator := strings.LastIndex(expectedTag, ":"); separator > strings.LastIndex(expectedTag, "/") {
			tagName = expectedTag[separator+1:]
		}
		for _, manifest := range index.Manifests {
			if ref, ok := manifest.Annotations["org.opencontainers.image.ref.name"]; ok && ref != "" && ref != expectedTag && ref != tagName {
				return errors.New("image archive contains an unexpected image tag")
			}
			// Docker's OCI export records the fully qualified image name.
			if name, ok := manifest.Annotations["io.containerd.image.name"]; ok && name != "" {
				if name != expectedTag && name != "docker.io/library/"+expectedTag {
					return errors.New("image archive contains an unexpected image tag")
				}
			}
		}
	}
	if len(repositoriesBytes) > 0 {
		var repositories map[string]map[string]string
		if err := json.Unmarshal(repositoriesBytes, &repositories); err != nil {
			return errors.New("image archive repositories file is invalid")
		}
		// Strict check: every repository entry must reference the expected tag.
		// The file format is {"redlaunch":{"vX.Y.Z":"sha256:..."}}.
		for repo, tags := range repositories {
			for tag := range tags {
				candidate := repo + ":" + tag
				if candidate != expectedTag {
					return errors.New("image archive contains an unexpected image tag")
				}
			}
		}
	}
	return nil
}

func readArchiveEntry(ctx context.Context, archivePath, name string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(contextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, errors.New("image archive is not a gzip archive")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(&boundedReader{reader: gzipReader, remaining: updateArchiveMaxUncompressed})
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.New("image archive validation timed out")
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("image archive entry is missing")
		}
		if err != nil {
			if errors.Is(err, errArchiveTooLarge) {
				return nil, errArchiveTooLarge
			}
			return nil, errors.New("image archive is invalid")
		}
		if strings.TrimPrefix(header.Name, "./") != name {
			continue
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
		default:
			return nil, errors.New("image archive entry is unsupported")
		}
		if header.Linkname != "" {
			return nil, errors.New("image archive entry is unsupported")
		}
		if header.Size > maxBytes {
			return nil, errors.New("image archive entry is too large")
		}
		contents, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
		if err != nil || int64(len(contents)) > maxBytes {
			return nil, errors.New("image archive entry could not be read")
		}
		return contents, nil
	}
}

func safeArchivePath(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return false
	}
	cleaned := path.Clean(name)
	return cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validateReleaseImage(manifest application.ReleaseManifest, arch string) (application.ReleaseImage, error) {
	image, ok := manifest.Images[arch]
	if !ok {
		return application.ReleaseImage{}, application.ErrReleaseIncompatible
	}
	if image.Asset == "" || image.SHA256 == "" || image.Tag == "" || image.Arch != arch {
		return application.ReleaseImage{}, application.ErrReleaseIncompatible
	}
	return image, nil
}

func validateLoadedImage(image compose.UpdateImageInfo, expected application.ReleaseImage, expectedVersion, expectedCommit string, expectedContract int) error {
	if image.ID == "" || image.ID != expected.ImageID {
		return errors.New("loaded image ID does not match the release manifest")
	}
	if image.OS != "linux" {
		return errors.New("loaded image is not a Linux image")
	}
	if image.Architecture == "" {
		return errors.New("loaded image architecture is missing")
	}
	if expected.Arch != "" && image.Architecture != expected.Arch {
		return errors.New("loaded image architecture does not match the release")
	}
	labels := image.Labels
	if labels["org.opencontainers.image.version"] != expectedVersion {
		return errors.New("loaded image version does not match the release")
	}
	if expectedCommit != "" && labels["org.opencontainers.image.revision"] != expectedCommit {
		return errors.New("loaded image revision does not match the release")
	}
	if expectedContract > 0 && labels["io.redlaunch.deployment-contract"] != fmt.Sprintf("%d", expectedContract) {
		return errors.New("loaded image deployment contract does not match")
	}
	return nil
}
