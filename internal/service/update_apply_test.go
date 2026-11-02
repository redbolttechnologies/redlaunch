package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type recordingUpdateDocker struct {
	targetImage      string
	version          string
	commit           string
	calls            []string
	inspects         int
	installDir       string
	projectsRoot     string
	projectName      string
	managerContainer string
	composeFile      string
	tagErr           error
}

func (d *recordingUpdateDocker) LoadImage(context.Context, string) error {
	d.calls = append(d.calls, "load")
	return nil
}
func (d *recordingUpdateDocker) InspectImage(context.Context, string) (compose.UpdateImageInfo, error) {
	d.calls = append(d.calls, "inspect-image")
	version := d.version
	if version == "" {
		version = "v1.1.0"
	}
	commit := d.commit
	if commit == "" {
		commit = "abc123"
	}
	return compose.UpdateImageInfo{ID: d.targetImage, OS: "linux", Architecture: "amd64", Labels: map[string]string{
		"org.opencontainers.image.version":  version,
		"org.opencontainers.image.revision": commit,
		"io.redlaunch.deployment-contract":  "1",
	}}, nil
}
func (d *recordingUpdateDocker) TagImage(_ context.Context, source, target string) error {
	d.calls = append(d.calls, "tag")
	return d.tagErr
}
func (d *recordingUpdateDocker) ContainerExists(context.Context, string) (bool, error) {
	return false, nil
}
func (d *recordingUpdateDocker) RemoveContainer(context.Context, string) error {
	d.calls = append(d.calls, "remove")
	return nil
}
func (d *recordingUpdateDocker) InspectContainer(context.Context, string) (compose.UpdateContainerInfo, error) {
	d.calls = append(d.calls, "inspect-container")
	d.inspects++
	var info compose.UpdateContainerInfo
	info.Name = "/redbolt-redlaunch"
	info.State.Running = true
	info.State.Status = "running"
	info.Config.Env = []string{
		"HTTP_ADDR=0.0.0.0:8080",
		"DB_PATH=/data/redlaunch.db",
		"PROJECTS_ROOT=" + d.projectsRoot,
		"REDLAUNCH_DIR=" + d.installDir,
	}
	info.Config.Labels = map[string]string{}
	if d.projectName != "" {
		info.Config.Labels["com.docker.compose.project"] = d.projectName
	}
	if d.composeFile != "" {
		info.Config.Labels["com.docker.compose.project.config_files"] = d.composeFile
	}
	if d.installDir != "" {
		projectName := d.projectName
		if projectName == "" {
			projectName = compose.ProjectName(d.installDir)
		}
		info.Mounts = []compose.UpdateMountInfo{
			{Type: "volume", Name: projectName + "_app-data", Source: "/var/lib/docker/volumes/" + projectName + "_app-data/_data", Destination: "/data"},
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
			{Type: "bind", Source: d.installDir, Destination: d.installDir},
			{Type: "bind", Source: d.projectsRoot, Destination: d.projectsRoot},
		}
	}
	if d.inspects == 1 {
		info.Image = "sha256:previous"
	} else {
		info.Image = d.targetImage
	}
	return info, nil
}
func (d *recordingUpdateDocker) ComposeConfig(_ context.Context, projectDir, projectName, composeFile, _ string) ([]byte, error) {
	d.calls = append(d.calls, "compose-config")
	installDir := d.installDir
	if installDir == "" {
		installDir = projectDir
	}
	projectsRoot := d.projectsRoot
	if projectsRoot == "" {
		projectsRoot = filepath.Dir(installDir)
	}
	managerContainer := d.managerContainer
	if managerContainer == "" {
		managerContainer = "redbolt-redlaunch"
	}
	// Minimal realistic deployment that satisfies the updater's deployment
	// contract: single app service, managed label, required mounts, and no
	// privileged overrides.
	return json.Marshal(map[string]any{
		"name": projectName,
		"volumes": map[string]any{
			"app-data": map[string]any{"name": projectName + "_app-data"},
		},
		"services": map[string]any{
			"app": map[string]any{
				"image":          d.targetImage,
				"container_name": managerContainer,
				"labels":         map[string]any{"redlaunch.managed": "true"},
				"environment": map[string]any{
					"HTTP_ADDR":       "0.0.0.0:8080",
					"DB_PATH":         "/data/redlaunch.db",
					"PROJECTS_ROOT":   projectsRoot,
					"REDLAUNCH_DIR":   installDir,
					"REDLAUNCH_IMAGE": "redlaunch:local",
				},
				"volumes": []any{
					map[string]any{"type": "volume", "source": "app-data", "target": "/data"},
					map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"},
					map[string]any{"type": "bind", "source": installDir, "target": installDir},
					map[string]any{"type": "bind", "source": projectsRoot, "target": projectsRoot},
				},
			},
		},
	})
}
func (d *recordingUpdateDocker) RecreateApp(context.Context, string, string, string, string) error {
	d.calls = append(d.calls, "recreate")
	return nil
}
func (d *recordingUpdateDocker) StartUpdateHelper(context.Context, string, string) error {
	d.calls = append(d.calls, "start-helper")
	return nil
}
func (d *recordingUpdateDocker) ExecHealth(context.Context, string, string) ([]byte, error) {
	d.calls = append(d.calls, "health")
	return []byte(`{"status":"ok","version":"v1.1.0","commit":"abc123"}`), nil
}

func TestCleanupDownloadOnlyRemovesOperationArtifacts(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{ProjectsRoot: t.TempDir(), Directory: t.TempDir(), Docker: &noopUpdateDocker{}})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	updater.cleanupDownload(application.UpdateState{OperationID: "op", DownloadPath: outside})
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was removed: %v", err)
	}
	inside := filepath.Join(updater.store.downloadDir("op"), "image.tar.gz")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	updater.cleanupDownload(application.UpdateState{OperationID: "op", DownloadPath: inside})
	if _, err := os.Stat(inside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation download was retained: %v", err)
	}
}

func TestRunApplyUpdateValidatesBeforeTagAndVerifiesReadiness(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := "sha256:target"
	docker := &recordingUpdateDocker{targetImage: target, installDir: installDir, projectsRoot: projectsRoot, projectName: "test-project", managerContainer: "redbolt-redlaunch", composeFile: filepath.Join(installDir, "docker-compose.yml")}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     projectsRoot,
		Directory:        installDir,
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "redbolt-redlaunch",
		Version:          "v1.0.0",
		Arch:             "amd64",
		Docker:           docker,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:             updateStateSchema,
		OperationID:        "op-1",
		Owner:              "helper",
		Type:               updateOperationType,
		Phase:              application.UpdatePhaseRestartQueued,
		Version:            "v1.1.0",
		Commit:             "abc123",
		DeploymentContract: 1,
		PreviousImageID:    "sha256:previous",
		TargetImageID:      target,
		RuntimeImage:       "redlaunch:local",
		InstallDir:         installDir,
		ComposeFile:        filepath.Join(installDir, "docker-compose.yml"),
		ProjectName:        "test-project",
		ManagerContainer:   "redbolt-redlaunch",
		HelperName:         updateHelperName,
		StartedAt:          time.Now().UTC(),
	}
	if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.RunApplyUpdate(context.Background(), ApplyUpdateOptions{OperationID: state.OperationID, StatePath: updater.store.statePath}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(docker.calls, ","); !strings.Contains(got, "compose-config,tag,recreate,inspect-container,health") {
		t.Fatalf("unexpected call order: %s", got)
	}
	got, ok, err := updater.store.readState()
	if err != nil || !ok || got.Phase != application.UpdatePhaseComplete || got.Acknowledged {
		t.Fatalf("final state = %#v, %t, %v", got, ok, err)
	}
	if updater.store.operationRunning() {
		t.Fatal("operation lock still held after successful update")
	}
}

func TestRunApplyUpdateRejectsPersistedStateThatDoesNotMatchInstallation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*application.UpdateState)
	}{
		{"install-directory", func(state *application.UpdateState) { state.InstallDir = t.TempDir() }},
		{"manager-container", func(state *application.UpdateState) { state.ManagerContainer = "other-manager" }},
		{"runtime-image", func(state *application.UpdateState) { state.RuntimeImage = "other:tag" }},
		{"deployment-contract", func(state *application.UpdateState) { state.DeploymentContract = 2 }},
		{"compose-file", func(state *application.UpdateState) { state.ComposeFile = filepath.Join(t.TempDir(), "compose.yml") }},
		{"download-path", func(state *application.UpdateState) { state.DownloadPath = filepath.Join(t.TempDir(), "image.tar.gz") }},
		{"release-version", func(state *application.UpdateState) { state.Version = "dev" }},
		{"release-commit", func(state *application.UpdateState) { state.Commit = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectsRoot := t.TempDir()
			installDir := t.TempDir()
			composeFile := filepath.Join(installDir, "docker-compose.yml")
			if err := os.WriteFile(composeFile, []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			docker := &recordingUpdateDocker{targetImage: "sha256:target", installDir: installDir, projectsRoot: projectsRoot, projectName: "test-project", managerContainer: "redbolt-redlaunch", composeFile: composeFile}
			updater, err := NewUpdateService(UpdateOptions{
				ProjectsRoot:     projectsRoot,
				Directory:        installDir,
				RuntimeImage:     "redlaunch:local",
				ManagerContainer: "redbolt-redlaunch",
				Version:          "v1.0.0",
				Arch:             "amd64",
				Docker:           docker,
			})
			if err != nil {
				t.Fatal(err)
			}
			state := application.UpdateState{
				Schema:             updateStateSchema,
				OperationID:        "mismatch-" + test.name,
				Owner:              "helper",
				Type:               updateOperationType,
				Phase:              application.UpdatePhaseRestartQueued,
				Version:            "v1.1.0",
				Commit:             "abc123",
				DeploymentContract: 1,
				PreviousImageID:    "sha256:previous",
				TargetImageID:      "sha256:target",
				RuntimeImage:       "redlaunch:local",
				InstallDir:         installDir,
				ComposeFile:        composeFile,
				ProjectName:        "test-project",
				ManagerContainer:   "redbolt-redlaunch",
				HelperName:         updateHelperName,
				StartedAt:          time.Now().UTC(),
			}
			test.mutate(&state)
			if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
				t.Fatal(err)
			}
			if err := updater.store.writeState(&state); err != nil {
				t.Fatal(err)
			}
			if err := updater.RunApplyUpdate(t.Context(), ApplyUpdateOptions{OperationID: state.OperationID}); !errors.Is(err, application.ErrUpdateUncertain) {
				t.Fatalf("mismatched state error = %v, want uncertain", err)
			}
			if len(docker.calls) != 0 {
				t.Fatalf("mismatched state mutated Docker: %v", docker.calls)
			}
			current, ok, err := updater.store.readState()
			if err != nil || !ok || current.Phase != application.UpdatePhaseUncertain {
				t.Fatalf("state = %#v, %t, %v", current, ok, err)
			}
		})
	}
}

type queueReleaseSource struct {
	archive string
}

func (s *queueReleaseSource) Latest(context.Context) (application.ReleaseInfo, error) {
	return application.ReleaseInfo{}, nil
}
func (s *queueReleaseSource) Download(_ context.Context, _ application.ReleaseAsset, destination, _ string, _ int64) error {
	contents, err := os.ReadFile(s.archive)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, contents, 0o600)
}

func TestQueueReleaseUpdateRejectsUnsafeAssetName(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := &recordingUpdateDocker{targetImage: "sha256:target", installDir: installDir, projectsRoot: projectsRoot, composeFile: composeFile}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     projectsRoot,
		Directory:        installDir,
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "redbolt-redlaunch",
		Version:          "v1.0.0",
		Arch:             "amd64",
		Docker:           docker,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := application.UpdateStatus{
		CurrentVersion:  "v1.0.0",
		LatestVersion:   "v1.1.0",
		UpdateAvailable: true,
		Compatible:      true,
		Release: &application.ReleaseInfo{
			Version: "v1.1.0",
			Manifest: application.ReleaseManifest{
				Version:            "v1.1.0",
				Commit:             "abc123",
				DeploymentContract: 1,
				Images: map[string]application.ReleaseImage{
					"amd64": {Arch: "amd64", Asset: "../image.tar.gz", SHA256: strings.Repeat("a", 64), ImageID: "sha256:target", Tag: "redlaunch:v1.1.0"},
				},
			},
		},
	}
	if err := updater.QueueReleaseUpdateWithProgress(t.Context(), status, nil); !errors.Is(err, application.ErrReleaseIncompatible) {
		t.Fatalf("unsafe asset name error = %v, want incompatible", err)
	}
	if len(docker.calls) != 0 {
		t.Fatalf("unsafe asset name reached Docker: %v", docker.calls)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseFailed {
		t.Fatalf("state = %#v, %t, %v", current, ok, err)
	}
}

func TestQueueReleaseUpdateLoadsVerifiedImageAndStartsHelper(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := writeImageArchiveFixture(t, "redlaunch:v1.2.3")
	target := "sha256:" + strings.Repeat("c", 64)
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	docker := &recordingUpdateDocker{targetImage: target, version: "v1.2.3", installDir: installDir, projectsRoot: projectsRoot, composeFile: composeFile}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     projectsRoot,
		Directory:        installDir,
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "redbolt-redlaunch",
		Version:          "v1.0.0",
		Arch:             "amd64",
		Docker:           docker,
		ReleaseSource:    &queueReleaseSource{archive: archive},
	})
	if err != nil {
		t.Fatal(err)
	}
	status := application.UpdateStatus{
		CurrentVersion:  "v1.0.0",
		LatestVersion:   "v1.2.3",
		UpdateAvailable: true,
		Compatible:      true,
		Release: &application.ReleaseInfo{
			Version: "v1.2.3",
			Manifest: application.ReleaseManifest{
				Version:            "v1.2.3",
				Commit:             "abc123",
				DeploymentContract: 1,
				Images: map[string]application.ReleaseImage{
					"amd64": {Arch: "amd64", Asset: "redlaunch-image-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64), ImageID: target, Tag: "redlaunch:v1.2.3"},
				},
			},
			Assets: []application.ReleaseAsset{{Name: "redlaunch-image-linux-amd64.tar.gz", URL: "https://github.com/example/image.tar.gz"}},
		},
	}
	if err := updater.QueueReleaseUpdateWithProgress(context.Background(), status, nil); err != nil {
		state, ok, stateErr := updater.store.readState()
		t.Fatalf("queue error: %v (state=%#v ok=%t readErr=%v)", err, state, ok, stateErr)
	}
	state, ok, err := updater.store.readState()
	if err != nil || !ok || state.Phase != application.UpdatePhaseRestartQueued || state.TargetImageID != target {
		t.Fatalf("state = %#v, %t, %v", state, ok, err)
	}
	if !updater.store.operationRunning() {
		t.Fatal("installation lock was released before the helper completed")
	}
	if got := strings.Join(docker.calls, ","); strings.Contains(got, "build") || !strings.Contains(got, "load") || !strings.Contains(got, "start-helper") {
		t.Fatalf("unexpected docker calls: %s", got)
	}
	_ = updater.store.release(state.OperationID)
}

func TestRunApplyUpdateRetainsLockWhenTagOutcomeIsAmbiguous(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	docker := &recordingUpdateDocker{
		targetImage:      "sha256:target",
		installDir:       installDir,
		projectsRoot:     projectsRoot,
		projectName:      "test-project",
		managerContainer: "redbolt-redlaunch",
		composeFile:      composeFile,
		tagErr:           context.DeadlineExceeded,
	}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       docker,
		Arch:         "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:             updateStateSchema,
		Revision:           1,
		OperationID:        "ambiguous-operation",
		Owner:              "helper",
		Type:               updateOperationType,
		Phase:              application.UpdatePhaseRestartQueued,
		Version:            "v1.1.0",
		Commit:             "abc123",
		DeploymentContract: 1,
		PreviousImageID:    "sha256:previous",
		TargetImageID:      "sha256:target",
		RuntimeImage:       "redlaunch:local",
		InstallDir:         installDir,
		ComposeFile:        composeFile,
		ProjectName:        "test-project",
		ManagerContainer:   "redbolt-redlaunch",
	}
	if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	err = updater.RunApplyUpdate(t.Context(), ApplyUpdateOptions{OperationID: state.OperationID})
	if !errors.Is(err, application.ErrUpdateUncertain) {
		t.Fatalf("tag ambiguity error = %v, want uncertain", err)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseUncertain {
		t.Fatalf("state = %#v, %t, %v", current, ok, err)
	}
	if !updater.UpdateInProgress() {
		t.Fatal("installation lock released while Docker outcome is uncertain")
	}
}

func TestRunApplyUpdateRequiresItsOwnInstallationLock(t *testing.T) {
	projectsRoot := t.TempDir()
	installDir := t.TempDir()
	composeFile := filepath.Join(installDir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := &recordingUpdateDocker{
		targetImage:      "sha256:target",
		installDir:       installDir,
		projectsRoot:     projectsRoot,
		projectName:      "test-project",
		managerContainer: "redbolt-redlaunch",
		composeFile:      composeFile,
	}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: projectsRoot,
		Directory:    installDir,
		Docker:       docker,
		Arch:         "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:             updateStateSchema,
		Revision:           1,
		OperationID:        "release-operation",
		Owner:              "helper",
		Type:               updateOperationType,
		Phase:              application.UpdatePhaseRestartQueued,
		Version:            "v1.1.0",
		Commit:             "abc123",
		DeploymentContract: 1,
		PreviousImageID:    "sha256:previous",
		TargetImageID:      "sha256:target",
		RuntimeImage:       "redlaunch:local",
		InstallDir:         installDir,
		ComposeFile:        composeFile,
		ProjectName:        "test-project",
		ManagerContainer:   "redbolt-redlaunch",
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.acquire("active-prune", "prune"); err != nil {
		t.Fatal(err)
	}
	err = updater.RunApplyUpdate(t.Context(), ApplyUpdateOptions{OperationID: state.OperationID})
	if !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("helper under a foreign lock error = %v, want busy", err)
	}
	if len(docker.calls) != 0 {
		t.Fatalf("helper mutated Docker without owning the lock: %v", docker.calls)
	}
}
