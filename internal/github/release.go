// Package github reads Redlaunch releases and release assets.
package github

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"redlaunch/internal/application"
)

const (
	// DefaultRepository is the official Redlaunch repository.
	DefaultRepository = "redbolttechnologies/redlaunch"

	defaultAPIBaseURL  = "https://api.github.com"
	manifestAssetName  = "redlaunch-release.json"
	checksumsAssetName = "SHA256SUMS"

	maxMetadataBytes = 2 * 1024 * 1024
	maxManifestBytes = 1024 * 1024
	maxChecksumsSize = 1024 * 1024
)

// Client reads releases from GitHub.
type Client struct {
	Repository string
	APIBaseURL string
	HTTP       *http.Client
}

// NewClient constructs a release client for the official repository.
func NewClient() *Client {
	return &Client{
		Repository: DefaultRepository,
		APIBaseURL: defaultAPIBaseURL,
		HTTP: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: validateRedirect,
		},
	}
}

type releaseResponse struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// Latest returns the newest stable release and its verified manifest. The
// image archives are not downloaded.
func (c *Client) Latest(ctx context.Context) (application.ReleaseInfo, error) {
	if c == nil {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	repository := strings.Trim(strings.TrimSpace(c.Repository), "/")
	if !validRepository(repository) {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	base := strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	if base == "" {
		base = defaultAPIBaseURL
	}
	endpoint := base + "/repos/" + repository + "/releases/latest"
	body, err := c.get(ctx, endpoint, maxMetadataBytes)
	if err != nil {
		return application.ReleaseInfo{}, err
	}
	var response releaseResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	version := strings.TrimSpace(response.TagName)
	if _, err := application.ParseReleaseVersion(version); err != nil {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	publishedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(response.PublishedAt))
	assets := make([]application.ReleaseAsset, 0, len(response.Assets))
	byName := make(map[string]application.ReleaseAsset, len(response.Assets))
	for _, asset := range response.Assets {
		name := strings.TrimSpace(asset.Name)
		url := strings.TrimSpace(asset.URL)
		if name == "" || url == "" {
			return application.ReleaseInfo{}, application.ErrReleaseUnavailable
		}
		item := application.ReleaseAsset{Name: name, URL: url, Size: asset.Size}
		assets = append(assets, item)
		byName[name] = item
	}
	manifestAsset, ok := byName[manifestAssetName]
	if !ok {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	manifestBytes, err := c.downloadBytes(ctx, manifestAsset, maxManifestBytes)
	if err != nil {
		return application.ReleaseInfo{}, err
	}
	checksums, err := c.downloadChecksums(ctx, byName[checksumsAssetName])
	if err != nil {
		return application.ReleaseInfo{}, err
	}
	manifestHash := sha256.Sum256(manifestBytes)
	if expected := checksums[manifestAssetName]; expected == "" || expected != hex.EncodeToString(manifestHash[:]) {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	var manifest application.ReleaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return application.ReleaseInfo{}, application.ErrReleaseUnavailable
	}
	if err := validateManifest(manifest, version, byName, checksums); err != nil {
		return application.ReleaseInfo{}, err
	}
	return application.ReleaseInfo{
		Version:     version,
		HTMLURL:     strings.TrimSpace(response.HTMLURL),
		PublishedAt: publishedAt,
		Manifest:    manifest,
		Assets:      assets,
		Checksums:   checksums,
	}, nil
}

// Download writes one release asset to destination and verifies its SHA-256.
// The destination is created with private permissions and replaced atomically.
func (c *Client) Download(ctx context.Context, asset application.ReleaseAsset, destination, expectedSHA256 string, maxBytes int64) error {
	if c == nil || strings.TrimSpace(asset.URL) == "" || strings.TrimSpace(destination) == "" {
		return application.ErrReleaseUnavailable
	}
	if maxBytes < 1 {
		return application.ErrReleaseUnavailable
	}
	assetURL, err := url.Parse(asset.URL)
	if err != nil || validateURL(assetURL) != nil {
		return application.ErrReleaseUnavailable
	}
	if asset.Size > maxBytes {
		return application.ErrReleaseUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return application.ErrReleaseUnavailable
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".redlaunch-download-*")
	if err != nil {
		return application.ErrReleaseUnavailable
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_ = tmp.Chmod(0o600)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "redlaunch-updater")
	downloadClient := *c.httpClient()
	downloadClient.Timeout = 0
	response, err := downloadClient.Do(request)
	if err != nil {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	hash := sha256.New()
	limit := &io.LimitedReader{R: response.Body, N: maxBytes + 1}
	written, err := io.Copy(io.MultiWriter(tmp, hash), limit)
	if err != nil || written > maxBytes {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	if expectedSHA256 != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(expectedSHA256)) {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return application.ErrReleaseUnavailable
	}
	if err := tmp.Close(); err != nil {
		return application.ErrReleaseUnavailable
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return application.ErrReleaseUnavailable
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, application.ErrReleaseUnavailable
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "redlaunch-updater")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, application.ErrReleaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, application.ErrReleaseUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, application.ErrReleaseUnavailable
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(contents)) > maxBytes {
		return nil, application.ErrReleaseUnavailable
	}
	return contents, nil
}

func (c *Client) downloadBytes(ctx context.Context, asset application.ReleaseAsset, maxBytes int64) ([]byte, error) {
	assetURL, err := url.Parse(asset.URL)
	if err != nil || validateURL(assetURL) != nil {
		return nil, application.ErrReleaseUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, application.ErrReleaseUnavailable
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "redlaunch-updater")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, application.ErrReleaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, application.ErrReleaseUnavailable
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(contents)) > maxBytes {
		return nil, application.ErrReleaseUnavailable
	}
	return contents, nil
}

func (c *Client) downloadChecksums(ctx context.Context, asset application.ReleaseAsset) (map[string]string, error) {
	if asset.Name == "" {
		return nil, application.ErrReleaseUnavailable
	}
	contents, err := c.downloadBytes(ctx, asset, maxChecksumsSize)
	if err != nil {
		return nil, err
	}
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		checksums[name] = strings.ToLower(fields[0])
	}
	if len(checksums) == 0 {
		return nil, application.ErrReleaseUnavailable
	}
	return checksums, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: validateRedirect}
}

func validateRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return validateURL(request.URL)
}

func validateURL(target *url.URL) error {
	if target == nil || target.Scheme != "https" || target.User != nil || target.Fragment != "" {
		return errors.New("release URL must use HTTPS without credentials, query, or fragment")
	}
	host := strings.ToLower(target.Hostname())
	if host == "github.com" || host == "api.github.com" {
		return nil
	}
	if strings.HasSuffix(host, ".githubusercontent.com") || host == "githubusercontent.com" {
		return nil
	}
	return errors.New("release URL host is not allowed")
}

func validateManifest(manifest application.ReleaseManifest, version string, assets map[string]application.ReleaseAsset, checksums map[string]string) error {
	if manifest.Version != version {
		return application.ErrReleaseUnavailable
	}
	if manifest.DeploymentContract < 1 {
		return application.ErrReleaseUnavailable
	}
	if strings.TrimSpace(manifest.Commit) == "" {
		return application.ErrReleaseUnavailable
	}
	if len(manifest.Images) == 0 {
		return application.ErrReleaseUnavailable
	}
	for arch, image := range manifest.Images {
		if arch != "amd64" && arch != "arm64" {
			return application.ErrReleaseUnavailable
		}
		if image.Arch != arch {
			return application.ErrReleaseUnavailable
		}
		if strings.TrimSpace(image.Asset) == "" || strings.TrimSpace(image.SHA256) == "" || strings.TrimSpace(image.Tag) == "" {
			return application.ErrReleaseUnavailable
		}
		if !validReleaseAssetName(image.Asset) {
			return application.ErrReleaseUnavailable
		}
		if _, ok := assets[image.Asset]; !ok {
			return application.ErrReleaseUnavailable
		}
		if len(image.SHA256) != 64 {
			return application.ErrReleaseUnavailable
		}
		if _, err := hex.DecodeString(image.SHA256); err != nil {
			return application.ErrReleaseUnavailable
		}
		if expected := checksums[image.Asset]; expected == "" || !strings.EqualFold(expected, image.SHA256) {
			return application.ErrReleaseUnavailable
		}
		if !strings.HasPrefix(image.ImageID, "sha256:") || len(image.ImageID) != len("sha256:")+64 {
			return application.ErrReleaseUnavailable
		}
		if image.Tag != "redlaunch:"+version {
			return application.ErrReleaseUnavailable
		}
	}
	return nil
}

// validReleaseAssetName keeps manifest asset names to a single file name.
// The updater joins the name to its private download directory, so separators
// or dot segments must never reach that path.
func validReleaseAssetName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) &&
		filepath.Base(name) == name
}

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
				return false
			}
		}
	}
	return true
}
