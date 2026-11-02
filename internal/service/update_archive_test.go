package service

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeImageArchiveFixture(t *testing.T, tag string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "image.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	write := func(name string, contents []byte) {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", []byte(`[{"Config":"config.json","RepoTags":["`+tag+`"],"Layers":["layer.tar"]}]`))
	write("config.json", []byte(`{"os":"linux","architecture":"amd64","config":{"Labels":{"org.opencontainers.image.version":"v1.2.3","org.opencontainers.image.revision":"abc123","io.redlaunch.deployment-contract":"1"}}}`))
	write("layer.tar", []byte("layer"))
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateImageArchive(t *testing.T) {
	path := writeImageArchiveFixture(t, "redlaunch:v1.2.3")
	ctx := t.Context()
	if err := validateImageArchive(ctx, path, "redlaunch:v1.2.3", "v1.2.3", "abc123", 1); err != nil {
		t.Fatalf("valid archive rejected: %v", err)
	}
	if err := validateImageArchive(ctx, path, "redlaunch:v9.9.9", "v1.2.3", "abc123", 1); err == nil {
		t.Fatal("archive with an unexpected tag accepted")
	}
	if err := validateImageArchive(ctx, path, "redlaunch:v1.2.3", "v1.2.4", "abc123", 1); err == nil {
		t.Fatal("archive with a mismatched version accepted")
	}
	if err := validateImageArchive(ctx, path, "redlaunch:v1.2.3", "v1.2.3", "abc999", 1); err == nil {
		t.Fatal("archive with a mismatched commit accepted")
	}
	if err := validateImageArchive(ctx, path, "redlaunch:v1.2.3", "v1.2.3", "abc123", 2); err == nil {
		t.Fatal("archive with a mismatched deployment contract accepted")
	}
}

func TestValidateArchiveReferencesAcceptsDockerSaveOCIAnnotation(t *testing.T) {
	index := []byte(`{"schemaVersion":2,"manifests":[{"annotations":{"io.containerd.image.name":"docker.io/library/redlaunch:v1.2.3","org.opencontainers.image.ref.name":"v1.2.3"}}]}`)
	if err := validateArchiveReferences(index, nil, "redlaunch:v1.2.3"); err != nil {
		t.Fatalf("Docker save OCI annotation rejected: %v", err)
	}
	wrong := []byte(`{"schemaVersion":2,"manifests":[{"annotations":{"io.containerd.image.name":"docker.io/library/other:v1.2.3"}}]}`)
	if err := validateArchiveReferences(wrong, nil, "redlaunch:v1.2.3"); err == nil {
		t.Fatal("unexpected OCI image name accepted")
	}
}

func TestBoundedReaderIncludesMetadataInLimit(t *testing.T) {
	reader := &boundedReader{reader: strings.NewReader(strings.Repeat("x", 64)), remaining: 16}
	if _, err := io.ReadAll(reader); !errors.Is(err, errArchiveTooLarge) {
		t.Fatalf("bounded reader error = %v, want archive-too-large", err)
	}
}

type ociArchiveOptions struct {
	missingIndexBlob bool
	tamperLayer      bool
	reverseLayers    bool
}

func writeOCIImageArchiveFixture(t *testing.T, tag string, options ociArchiveOptions) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "image.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	write := func(name string, contents []byte) {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	blob := func(sum [32]byte) string { return "blobs/sha256/" + hex.EncodeToString(sum[:]) }

	config := []byte(`{"os":"linux","architecture":"amd64","config":{"Labels":{"org.opencontainers.image.version":"v1.2.3","org.opencontainers.image.revision":"abc123","io.redlaunch.deployment-contract":"1"}}}`)
	layers := [][]byte{[]byte("layer one contents"), []byte("layer two contents")}
	configSum := sha256.Sum256(config)
	configPath := blob(configSum)
	layerPaths := make([]string, len(layers))
	layerDescriptors := make([]any, len(layers))
	for index, layer := range layers {
		sum := sha256.Sum256(layer)
		layerPaths[index] = blob(sum)
		layerDescriptors[index] = map[string]any{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    "sha256:" + hex.EncodeToString(sum[:]),
			"size":      len(layer),
		}
	}
	if options.reverseLayers {
		slices.Reverse(layerDescriptors)
	}
	manifestBytes, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:" + hex.EncodeToString(configSum[:]),
			"size":      len(config),
		},
		"layers": layerDescriptors,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	manifestPath := blob(manifestSum)

	indexBlobBytes, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest":    "sha256:" + hex.EncodeToString(manifestSum[:]),
			"size":      len(manifestBytes),
			"platform":  map[string]string{"architecture": "amd64", "os": "linux"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexSum := sha256.Sum256(indexBlobBytes)
	indexPath := blob(indexSum)
	indexDigest := "sha256:" + hex.EncodeToString(indexSum[:])
	if options.missingIndexBlob {
		indexDigest = "sha256:" + strings.Repeat("f", 64)
	}
	indexJSON, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []any{map[string]any{
			"mediaType": "application/vnd.oci.image.index.v1+json",
			"digest":    indexDigest,
			"size":      len(indexBlobBytes),
			"annotations": map[string]string{
				"io.containerd.image.name":          "docker.io/library/" + tag,
				"org.opencontainers.image.ref.name": strings.TrimPrefix(tag, "redlaunch:"),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	classic, err := json.Marshal([]map[string]any{{
		"Config":   configPath,
		"RepoTags": []string{tag},
		"Layers":   layerPaths,
	}})
	if err != nil {
		t.Fatal(err)
	}

	write("manifest.json", classic)
	write("index.json", indexJSON)
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	write(configPath, config)
	for index, layer := range layers {
		if options.tamperLayer && index == 0 {
			layer = []byte("layer ONE contents")
		}
		write(layerPaths[index], layer)
	}
	write(manifestPath, manifestBytes)
	if !options.missingIndexBlob {
		write(indexPath, indexBlobBytes)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateImageArchiveValidatesOCIGraph(t *testing.T) {
	valid := writeOCIImageArchiveFixture(t, "redlaunch:v1.2.3", ociArchiveOptions{})
	if err := validateImageArchive(t.Context(), valid, "redlaunch:v1.2.3", "v1.2.3", "abc123", 1); err != nil {
		t.Fatalf("valid OCI save archive rejected: %v", err)
	}
	broken := writeOCIImageArchiveFixture(t, "redlaunch:v1.2.3", ociArchiveOptions{missingIndexBlob: true})
	if err := validateImageArchive(t.Context(), broken, "redlaunch:v1.2.3", "v1.2.3", "abc123", 1); err == nil {
		t.Fatal("archive accepted an OCI index referencing a missing image graph")
	}
	tampered := writeOCIImageArchiveFixture(t, "redlaunch:v1.2.3", ociArchiveOptions{tamperLayer: true})
	if err := validateImageArchive(t.Context(), tampered, "redlaunch:v1.2.3", "v1.2.3", "abc123", 1); err == nil {
		t.Fatal("archive accepted a layer whose contents do not match its digest")
	}
	reordered := writeOCIImageArchiveFixture(t, "redlaunch:v1.2.3", ociArchiveOptions{reverseLayers: true})
	if err := validateImageArchive(t.Context(), reordered, "redlaunch:v1.2.3", "v1.2.3", "abc123", 1); err == nil {
		t.Fatal("archive accepted an OCI manifest with a different layer order")
	}
}
