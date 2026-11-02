package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

func TestSameUpdatePathNormalizesDockerDesktopHostMounts(t *testing.T) {
	if _, err := os.Stat("/host_mnt"); err == nil {
		t.Skip("this host has a real /host_mnt tree")
	}
	if !sameUpdatePath("/host_mnt/private/example/data", "/private/example/data") {
		t.Fatal("Docker Desktop host-mount form was not recognized")
	}
	if sameUpdatePath("/host_mnt/private/example/data", "/private/example/other") {
		t.Fatal("unrelated Docker Desktop host-mount path accepted")
	}
	if sameUpdatePath("/host_mnt/etc", "/etc") {
		t.Fatal("arbitrary host path normalized through /host_mnt")
	}
}

func TestValidUpdateAssetName(t *testing.T) {
	for _, name := range []string{"redlaunch-image-linux-amd64.tar.gz", "release.tar.gz"} {
		if !validUpdateAssetName(name) {
			t.Fatalf("valid asset name %q rejected", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../image.tar.gz", "nested/image.tar.gz", `nested\image.tar.gz`} {
		if validUpdateAssetName(name) {
			t.Fatalf("unsafe asset name %q accepted", name)
		}
	}
}

func TestNormalizeDockerDesktopSocketMount(t *testing.T) {
	if _, err := os.Stat("/run/host-services/docker.proxy.sock"); err == nil {
		t.Skip("this host has a real Docker Desktop proxy socket")
	}
	compose := map[string]updateMount{
		"/var/run/docker.sock": {Type: "bind", Source: "/var/run/docker.sock"},
	}
	if got := normalizeDockerDesktopSocketMount("/run/host-services/docker.proxy.sock", "/var/run/docker.sock", compose, "/var/run/docker.sock"); got != "/var/run/docker.sock" {
		t.Fatalf("socket proxy normalized to %q", got)
	}
	changed := map[string]updateMount{
		"/var/run/docker.sock": {Type: "bind", Source: "/run/other.sock"},
	}
	if got := normalizeDockerDesktopSocketMount("/run/host-services/docker.proxy.sock", "/var/run/docker.sock", changed, "/var/run/docker.sock"); got != "/run/host-services/docker.proxy.sock" {
		t.Fatalf("changed socket source normalized to %q", got)
	}
}

func TestValidateLiveComposeFilesAcceptsUpdaterOverride(t *testing.T) {
	base := filepath.Join(t.TempDir(), "docker-compose.yml")
	override := filepath.Join(t.TempDir(), "core", "updater", "app-image.yml")
	var info compose.UpdateContainerInfo
	info.Config.Labels = map[string]string{
		"com.docker.compose.project.config_files": base + "," + override,
	}
	if err := validateLiveComposeFiles(info, base, override); err != nil {
		t.Fatalf("updater-owned override rejected on a repeated update: %v", err)
	}
	info.Config.Labels["com.docker.compose.project.config_files"] = base + "," + filepath.Join(t.TempDir(), "unexpected.yml")
	if err := validateLiveComposeFiles(info, base, override); err == nil {
		t.Fatal("unrelated Compose override accepted")
	}
}

func TestValidateDeploymentRejectsWrongMountSourcesAndEphemeralDatabase(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	config, err := json.Marshal(map[string]any{
		"name": "test-project",
		"services": map[string]any{
			"app": map[string]any{
				"image":          "redlaunch:local",
				"container_name": "redbolt-redlaunch",
				"labels":         map[string]string{"redlaunch.managed": "true"},
				"environment": map[string]string{
					"HTTP_ADDR":     ":8080",
					"DB_PATH":       "/tmp/ephemeral.db",
					"PROJECTS_ROOT": projectsRoot,
					"REDLAUNCH_DIR": installDir,
				},
				"volumes": []map[string]string{
					{"type": "bind", "source": "/wrong/database", "target": "/data"},
					{"type": "bind", "source": "/wrong/socket", "target": "/var/run/docker.sock"},
					{"type": "bind", "source": "/wrong/installation", "target": installDir},
					{"type": "bind", "source": "/wrong/projects", "target": projectsRoot},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	docker := &reviewDeploymentDocker{config: config}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       docker,
	})
	if err != nil {
		t.Fatal(err)
	}
	var live compose.UpdateContainerInfo
	live.Config.Env = []string{
		"HTTP_ADDR=:8080",
		"DB_PATH=/tmp/ephemeral.db",
		"PROJECTS_ROOT=" + projectsRoot,
		"REDLAUNCH_DIR=" + installDir,
	}
	live.Mounts = []compose.UpdateMountInfo{
		{Type: "bind", Source: "/wrong/database", Destination: "/data"},
		{Type: "bind", Source: "/wrong/socket", Destination: "/var/run/docker.sock"},
		{Type: "bind", Source: "/wrong/installation", Destination: installDir},
		{Type: "bind", Source: "/wrong/projects", Destination: projectsRoot},
	}
	err = updater.validateDeployment(t.Context(), application.UpdateState{
		ComposeFile:      filepath.Join(installDir, "docker-compose.yml"),
		ProjectName:      "test-project",
		ManagerContainer: "redbolt-redlaunch",
	}, live)
	if !errors.Is(err, application.ErrReleaseIncompatible) {
		t.Fatalf("wrong mount sources or ephemeral database accepted: %v", err)
	}
}

type reviewDeploymentDocker struct {
	noopUpdateDocker
	config []byte
}

func (d *reviewDeploymentDocker) ComposeConfig(context.Context, string, string, string, string) ([]byte, error) {
	return d.config, nil
}

func TestValidateDeploymentRejectsRuntimeIdentityMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any, *compose.UpdateContainerInfo)
	}{
		{"app-image-tag", func(config map[string]any, _ *compose.UpdateContainerInfo) {
			config["services"].(map[string]any)["app"].(map[string]any)["image"] = "other:tag"
		}},
		{"app-image-digest", func(config map[string]any, _ *compose.UpdateContainerInfo) {
			config["services"].(map[string]any)["app"].(map[string]any)["image"] = "redlaunch@sha256:" + strings.Repeat("a", 64)
		}},
		{"compose-runtime-image", func(config map[string]any, _ *compose.UpdateContainerInfo) {
			app := config["services"].(map[string]any)["app"].(map[string]any)
			app["environment"].(map[string]any)["REDLAUNCH_IMAGE"] = "other:tag"
		}},
		{"compose-container-name", func(config map[string]any, _ *compose.UpdateContainerInfo) {
			app := config["services"].(map[string]any)["app"].(map[string]any)
			app["environment"].(map[string]any)["REDLAUNCH_CONTAINER_NAME"] = "other-manager"
		}},
		{"live-runtime-image", func(_ map[string]any, live *compose.UpdateContainerInfo) {
			live.Config.Env = append(live.Config.Env, "REDLAUNCH_IMAGE=other:tag")
		}},
		{"live-container-name", func(_ map[string]any, live *compose.UpdateContainerInfo) {
			live.Config.Env = append(live.Config.Env, "REDLAUNCH_CONTAINER_NAME=other-manager")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectsRoot := t.TempDir()
			installDir := t.TempDir()
			composeFile := filepath.Join(installDir, "docker-compose.yml")
			recorder := &recordingUpdateDocker{targetImage: "sha256:target", installDir: installDir, projectsRoot: projectsRoot, projectName: "test-project", composeFile: composeFile}
			live, err := recorder.InspectContainer(t.Context(), "redbolt-redlaunch")
			if err != nil {
				t.Fatal(err)
			}
			contents, err := recorder.ComposeConfig(t.Context(), installDir, "test-project", composeFile, "")
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(contents, &config); err != nil {
				t.Fatal(err)
			}
			test.mutate(config, &live)
			changed, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			updater, err := NewUpdateService(UpdateOptions{
				ProjectsRoot:     projectsRoot,
				Directory:        installDir,
				RuntimeImage:     "redlaunch:local",
				ManagerContainer: "redbolt-redlaunch",
				Docker:           &reviewDeploymentDocker{config: changed},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = updater.validateDeployment(t.Context(), application.UpdateState{
				ComposeFile:      composeFile,
				ProjectName:      "test-project",
				ManagerContainer: "redbolt-redlaunch",
			}, live)
			if !errors.Is(err, application.ErrReleaseIncompatible) {
				t.Fatalf("runtime identity mismatch accepted: %v", err)
			}
		})
	}
}

func TestValidateDeploymentRejectsChangedDatabaseVolumeIdentity(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	recorder := &recordingUpdateDocker{
		targetImage:  "sha256:target",
		installDir:   installDir,
		projectsRoot: projectsRoot,
		projectName:  "test-project",
		composeFile:  composeFile,
	}
	live, err := recorder.InspectContainer(t.Context(), "redbolt-redlaunch")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := recorder.ComposeConfig(t.Context(), installDir, "test-project", composeFile, "")
	if err != nil {
		t.Fatal(err)
	}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       &reviewDeploymentDocker{config: contents},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		ComposeFile:      composeFile,
		ProjectName:      "test-project",
		ManagerContainer: "redbolt-redlaunch",
	}
	if err := updater.validateDeployment(t.Context(), state, live); err != nil {
		t.Fatalf("valid deployment rejected: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	volumes, ok := config["volumes"].(map[string]any)
	if !ok {
		t.Fatal("fixture volumes are missing")
	}
	appData, ok := volumes["app-data"].(map[string]any)
	if !ok {
		t.Fatal("fixture app-data volume is missing")
	}
	appData["name"] = "unrelated-empty-volume"
	changed, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	updater, err = NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       &reviewDeploymentDocker{config: changed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.validateDeployment(t.Context(), state, live); !errors.Is(err, application.ErrReleaseIncompatible) {
		t.Fatalf("replacement database volume accepted: %v", err)
	}
}

func TestValidateDeploymentRejectsDatabaseVolumeSubpath(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	recorder := &recordingUpdateDocker{
		targetImage:  "sha256:target",
		installDir:   installDir,
		projectsRoot: projectsRoot,
		projectName:  "test-project",
		composeFile:  composeFile,
	}
	live, err := recorder.InspectContainer(t.Context(), "redbolt-redlaunch")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := recorder.ComposeConfig(t.Context(), installDir, "test-project", composeFile, "")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	app, ok := config["services"].(map[string]any)["app"].(map[string]any)
	if !ok {
		t.Fatal("fixture app service is missing")
	}
	volumes, ok := app["volumes"].([]any)
	if !ok || len(volumes) == 0 {
		t.Fatal("fixture app volumes are missing")
	}
	mount, ok := volumes[0].(map[string]any)
	if !ok {
		t.Fatal("fixture /data mount is invalid")
	}
	mount["volume"] = map[string]any{"subpath": "different-database-directory"}
	changed, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       &reviewDeploymentDocker{config: changed},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = updater.validateDeployment(t.Context(), application.UpdateState{
		ComposeFile:      composeFile,
		ProjectName:      "test-project",
		ManagerContainer: "redbolt-redlaunch",
	}, live)
	if !errors.Is(err, application.ErrReleaseIncompatible) {
		t.Fatalf("database volume subpath accepted: %v", err)
	}
}
