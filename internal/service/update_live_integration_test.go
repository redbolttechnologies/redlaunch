package service

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

// TestReleaseUpdateAcceptance exercises the real detached helper and Compose
// replacement path with disposable images and containers. It intentionally
// builds three real Redlaunch images so health, tagging, archive validation,
// and only-app recreation are tested end to end.
func TestReleaseUpdateAcceptance(t *testing.T) {
	if os.Getenv("REDLAUNCH_UPDATE_INTEGRATION") != "1" {
		t.Skip("set REDLAUNCH_UPDATE_INTEGRATION=1 to run disposable release-update checks")
	}
	if runtime.GOOS == "windows" {
		t.Skip("disposable release-update checks require a Unix host")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for the release-update acceptance check: %v", err)
	}
	if output, err := acceptanceDockerRun(context.Background(), "ps", "-a", "--filter", "name=^/"+updateHelperName+"$", "--format", "{{.Names}}"); err != nil {
		t.Fatalf("inspect Docker before release-update acceptance: %v", err)
	} else if strings.TrimSpace(output) != "" {
		t.Fatalf("container %s already exists; remove it before running the acceptance check", updateHelperName)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	project := "redlaunch-accept-" + suffix
	manager := "redlaunch-accept-" + suffix
	runtimeTag := "redlaunch-accept-" + suffix + ":runtime"
	commit := "acceptance" + suffix
	arch := runtime.GOARCH
	buildDate := "2026-11-02T00:00:00Z"
	installDir := t.TempDir()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// Docker Desktop reports canonical /private/var paths on macOS. Resolve
	// the temporary-directory symlink so Compose and inspection agree.
	if installDir, err = filepath.EvalSymlinks(installDir); err != nil {
		t.Fatal(err)
	}
	if projectsRoot, err = filepath.EvalSymlinks(projectsRoot); err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(installDir, "compose.yml")
	composeContents := fmt.Sprintf(`name: %s
services:
  app:
    image: %s
    pull_policy: never
    container_name: %s
    labels:
      redlaunch.managed: "true"
    env_file:
      - vars.env
      - secrets.env
    environment:
      HTTP_ADDR: ":8080"
      DB_PATH: "/data/redlaunch.db"
      PROJECTS_ROOT: %q
      REDLAUNCH_DIR: %q
      REDLAUNCH_IMAGE: %q
      REDLAUNCH_CONTAINER_NAME: %q
      BACKUP_ROOT: "/data/backups"
    volumes:
      - type: volume
        source: data
        target: /data
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
      - type: bind
        source: %q
        target: %q
      - type: bind
        source: %q
        target: %q
volumes:
  data:
`, project, runtimeTag, manager, projectsRoot, installDir, runtimeTag, manager, installDir, installDir, projectsRoot, projectsRoot)
	for name, contents := range map[string]string{
		"compose.yml": composeContents,
		"vars.env":    "",
		"secrets.env": "AUTH_SESSION_SECRET=disposable-release-update-session-secret\n",
	} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	bystander := manager + "-bystander"
	bystanderVolume := manager + "-bystander-data"
	dataVolume := project + "_data"
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_, _ = acceptanceDockerRun(cleanup, "compose", "--project-directory", installDir, "--project-name", project, "-f", composeFile, "down", "--volumes", "--remove-orphans")
		_, _ = acceptanceDockerRun(cleanup, "rm", "-f", manager, bystander, updateHelperName)
		_, _ = acceptanceDockerRun(cleanup, "volume", "rm", "-f", bystanderVolume, dataVolume)
		_, _ = acceptanceDockerRun(cleanup, "network", "rm", updateHelperProjectName+"_default")
		for _, tag := range []string{
			runtimeTag,
			"redlaunch-accept-" + suffix + ":v0.0.1",
			"redlaunch-accept-" + suffix + ":v0.0.2",
			"redlaunch-accept-" + suffix + ":v0.0.3",
			"redlaunch-accept-" + suffix + ":v0.0.4",
			"redlaunch-accept-" + suffix + ":v0.0.5",
		} {
			_, _ = acceptanceDockerRun(cleanup, "image", "rm", "-f", tag)
		}
	})

	versionA := "v0.0.1"
	versionB := "v0.0.2"
	versionC := "v0.0.3"
	releaseA := acceptanceBuildRelease(t, ctx, repoRoot, suffix, arch, commit, buildDate, versionA)
	releaseB := acceptanceBuildRelease(t, ctx, repoRoot, suffix, arch, commit, buildDate, versionB)
	releaseC := acceptanceBuildRelease(t, ctx, repoRoot, suffix, arch, commit, buildDate, versionC)
	acceptanceDocker(t, ctx, "tag", releaseA.info.Manifest.Images[arch].Tag, runtimeTag)

	// Redlaunch refuses to serve without an authenticated operator. Seed the
	// disposable database before Compose starts the manager.
	acceptanceDocker(t, ctx, "volume", "create", dataVolume)
	seed := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--platform", "linux/"+arch, "--volume", dataVolume+":/data", releaseA.info.Manifest.Images[arch].Tag, "auth-create-user", "--email", "acceptance@example.com", "--password-stdin", "--db-path", "/data/redlaunch.db")
	seed.Stdin = strings.NewReader("Acceptance-Password-123!\n")
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed disposable local user: %v\n%s", err, output)
	}

	acceptanceDocker(t, ctx, "compose", "--project-directory", installDir, "--project-name", project, "-f", composeFile, "up", "-d", "--no-build")
	acceptanceWaitHealth(t, ctx, manager, versionA, commit)
	acceptanceWaitContainerState(t, ctx, manager, true)
	acceptanceDocker(t, ctx, "exec", manager, "sh", "-c", "printf acceptance-data > /data/marker")
	managerBefore := acceptanceDockerOutput(t, ctx, "inspect", "--format", "{{.Id}}", manager)
	acceptanceDocker(t, ctx, "run", "-d", "--name", bystander, "--volume", bystanderVolume+":/work", "alpine:3.22", "sleep", "3600")
	acceptanceDocker(t, ctx, "exec", bystander, "sh", "-c", "printf bystander-data > /work/marker")
	bystanderID := acceptanceDockerOutput(t, ctx, "inspect", "--format", "{{.Id}}", bystander)

	runRelease := func(currentVersion string, release acceptanceRelease, operationID string) (application.UpdateState, *UpdateService) {
		t.Helper()
		updater, err := NewUpdateService(UpdateOptions{
			ProjectsRoot:     projectsRoot,
			Directory:        installDir,
			RuntimeImage:     runtimeTag,
			ManagerContainer: manager,
			Docker:           compose.CommandRunner{},
			ReleaseSource:    &acceptanceReleaseSource{info: release.info, archives: map[string]string{release.info.Manifest.Images[arch].Asset: release.archive}},
			Arch:             arch,
			Version:          currentVersion,
		})
		if err != nil {
			t.Fatal(err)
		}
		status := application.UpdateStatus{
			CurrentVersion:  currentVersion,
			LatestVersion:   release.info.Version,
			UpdateAvailable: true,
			Compatible:      true,
			Release:         &release.info,
		}
		if err := updater.QueueReleaseUpdateWithOperationID(ctx, operationID, status, nil); err != nil {
			state, _, _ := updater.UpdateState(ctx)
			t.Fatalf("queue %s: %v (state phase=%s error=%q)", release.info.Version, err, state.Phase, state.Error)
		}
		state := acceptanceWaitTerminal(t, ctx, updater, operationID)
		return state, updater
	}

	stateB, _ := runRelease(versionA, releaseB, "acceptance-"+suffix+"-b")
	acceptanceRequireComplete(t, stateB)
	acceptanceAssertTransition(t, ctx, manager, bystander, bystanderID, project, runtimeTag, versionB, commit, releaseB.info, managerBefore, true)
	managerBefore = acceptanceDockerOutput(t, ctx, "inspect", "--format", "{{.Id}}", manager)

	stateC, _ := runRelease(versionB, releaseC, "acceptance-"+suffix+"-c")
	acceptanceRequireComplete(t, stateC)
	acceptanceAssertTransition(t, ctx, manager, bystander, bystanderID, project, runtimeTag, versionC, commit, releaseC.info, managerBefore, true)

	// A deliberate readiness failure: the image label says v0.0.4 while the
	// binary inside still reports v0.0.3. The helper must retain uncertain
	// state, the installation lock, and recovery artifacts.
	releaseD := acceptanceBuildBadReadinessRelease(t, ctx, suffix, arch, commit, buildDate, "v0.0.4", releaseC.info.Manifest.Images[arch].Tag)
	stateD, updaterD := runRelease(versionC, releaseD, "acceptance-"+suffix+"-d")
	if stateD.Phase != application.UpdatePhaseUncertain {
		t.Fatalf("readiness failure phase = %s, want uncertain (error %q)", stateD.Phase, stateD.Error)
	}
	if err := updaterD.store.requireUpdateLock(stateD.OperationID); err != nil {
		t.Fatalf("readiness failure did not retain the installation lock: %v", err)
	}
	if _, err := os.Stat(stateD.DownloadPath); err != nil {
		t.Fatalf("readiness failure removed the downloaded archive: %v", err)
	}
	if _, err := os.Stat(updaterD.store.appOverridePath()); err != nil {
		t.Fatalf("readiness failure removed the Compose override: %v", err)
	}
	if err := updaterD.ClearUpdateState(ctx, stateD.OperationID, true); err != nil {
		t.Fatalf("acknowledge readiness failure: %v", err)
	}
	if updaterD.UpdateInProgress() {
		t.Fatal("acknowledged readiness failure retained the installation lock")
	}

	// An interrupted handoff: the helper is killed before it can mutate app.
	// Startup reconciliation must retain uncertain state and artifacts.
	releaseE := acceptanceBuildSleepingHelperRelease(t, ctx, suffix, arch, commit, buildDate, "v0.0.5")
	updaterE, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     projectsRoot,
		Directory:        installDir,
		RuntimeImage:     runtimeTag,
		ManagerContainer: manager,
		Docker:           compose.CommandRunner{},
		ReleaseSource:    &acceptanceReleaseSource{info: releaseE.info, archives: map[string]string{releaseE.info.Manifest.Images[arch].Asset: releaseE.archive}},
		Arch:             arch,
		Version:          "v0.0.4",
	})
	if err != nil {
		t.Fatal(err)
	}
	statusE := application.UpdateStatus{CurrentVersion: "v0.0.4", LatestVersion: releaseE.info.Version, UpdateAvailable: true, Compatible: true, Release: &releaseE.info}
	operationE := "acceptance-" + suffix + "-e"
	if err := updaterE.QueueReleaseUpdateWithOperationID(ctx, operationE, statusE, nil); err != nil {
		t.Fatalf("queue interrupted handoff: %v", err)
	}
	acceptanceWaitHelperRunning(t, ctx)
	if err := updaterE.ReconcileUpdateState(ctx); err != nil {
		t.Fatalf("reconcile while helper runs: %v", err)
	}
	acceptanceDocker(t, ctx, "kill", updateHelperName)
	acceptanceWaitContainerState(t, ctx, updateHelperName, false)
	if err := updaterE.ReconcileInterruptedUpdate(ctx); !errors.Is(err, application.ErrUpdateBusy) && !errors.Is(err, application.ErrUpdateUncertain) {
		t.Fatalf("interrupted handoff reconciliation = %v", err)
	}
	stateE, ok, err := updaterE.UpdateState(ctx)
	if err != nil || !ok || stateE.OperationID != operationE || stateE.Phase != application.UpdatePhaseUncertain {
		t.Fatalf("interrupted handoff state = %#v, %v", stateE, err)
	}
	if err := updaterE.store.requireUpdateLock(operationE); err != nil {
		t.Fatalf("interrupted handoff did not retain the installation lock: %v", err)
	}
	if _, err := os.Stat(stateE.DownloadPath); err != nil {
		t.Fatalf("interrupted handoff removed the downloaded archive: %v", err)
	}
	if err := updaterE.ClearUpdateState(ctx, operationE, true); err != nil {
		t.Fatalf("acknowledge interrupted handoff: %v", err)
	}
	if updaterE.UpdateInProgress() {
		t.Fatal("acknowledged interrupted handoff retained the installation lock")
	}
}

type acceptanceReleaseSource struct {
	info     application.ReleaseInfo
	archives map[string]string
}

func (s *acceptanceReleaseSource) Latest(context.Context) (application.ReleaseInfo, error) {
	return s.info, nil
}

func (s *acceptanceReleaseSource) Download(ctx context.Context, asset application.ReleaseAsset, destination, expectedSHA string, maxBytes int64) error {
	source, ok := s.archives[asset.Name]
	if !ok {
		return errors.New("acceptance archive is not configured")
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(file, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return errors.New("acceptance archive copy failed")
	}
	if written > maxBytes || hex.EncodeToString(hash.Sum(nil)) != expectedSHA {
		_ = os.Remove(destination)
		return errors.New("acceptance archive checksum failed")
	}
	select {
	case <-ctx.Done():
		_ = os.Remove(destination)
		return ctx.Err()
	default:
	}
	return nil
}

type acceptanceRelease struct {
	info    application.ReleaseInfo
	archive string
}

func acceptanceBuildRelease(t *testing.T, ctx context.Context, repoRoot, suffix, arch, commit, buildDate, version string) acceptanceRelease {
	t.Helper()
	tag := "redlaunch-accept-" + suffix + ":" + version
	acceptanceDocker(t, ctx, "build",
		"--platform", "linux/"+arch,
		"--build-arg", "GO_VERSION=1.26.10",
		"--build-arg", "VERSION="+version,
		"--build-arg", "COMMIT="+commit,
		"--build-arg", "BUILD_DATE="+buildDate,
		"-t", tag,
		repoRoot,
	)
	imageID := acceptanceImageID(t, ctx, tag)
	archive := acceptanceSaveImage(t, ctx, tag)
	return acceptanceRelease{info: acceptanceReleaseInfo(t, version, commit, arch, tag, imageID, archive), archive: archive}
}

func acceptanceBuildBadReadinessRelease(t *testing.T, ctx context.Context, suffix, arch, commit, buildDate, labelVersion, baseTag string) acceptanceRelease {
	t.Helper()
	tag := "redlaunch-accept-" + suffix + ":" + labelVersion
	directory := t.TempDir()
	dockerfile := fmt.Sprintf("FROM %s\nLABEL org.opencontainers.image.version=%q org.opencontainers.image.revision=%q org.opencontainers.image.created=%q io.redlaunch.deployment-contract=\"1\"\n", baseTag, labelVersion, commit, buildDate)
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	acceptanceDocker(t, ctx, "build", "--platform", "linux/"+arch, "-t", tag, directory)
	archive := acceptanceSaveImage(t, ctx, tag)
	return acceptanceRelease{info: acceptanceReleaseInfo(t, labelVersion, commit, arch, tag, acceptanceImageID(t, ctx, tag), archive), archive: archive}
}

func acceptanceBuildSleepingHelperRelease(t *testing.T, ctx context.Context, suffix, arch, commit, buildDate, version string) acceptanceRelease {
	t.Helper()
	tag := "redlaunch-accept-" + suffix + ":" + version
	directory := t.TempDir()
	dockerfile := fmt.Sprintf(`FROM alpine:3.22
LABEL io.redlaunch.build.go-version="1.26.10" org.opencontainers.image.version=%q org.opencontainers.image.revision=%q org.opencontainers.image.created=%q io.redlaunch.deployment-contract="1"
RUN printf '#!/bin/sh\necho acceptance helper sleeping\nsleep 300\n' > /usr/local/bin/redlaunch && chmod +x /usr/local/bin/redlaunch
ENTRYPOINT ["/usr/local/bin/redlaunch"]
`, version, commit, buildDate)
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	acceptanceDocker(t, ctx, "build", "--platform", "linux/"+arch, "-t", tag, directory)
	archive := acceptanceSaveImage(t, ctx, tag)
	return acceptanceRelease{info: acceptanceReleaseInfo(t, version, commit, arch, tag, acceptanceImageID(t, ctx, tag), archive), archive: archive}
}

func acceptanceReleaseInfo(t *testing.T, version, commit, arch, tag, imageID, archive string) application.ReleaseInfo {
	t.Helper()
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	asset := "redlaunch-image-linux-" + arch + ".tar.gz"
	image := application.ReleaseImage{Arch: arch, Asset: asset, SHA256: hex.EncodeToString(sum[:]), ImageID: imageID, Tag: tag}
	return application.ReleaseInfo{
		Version: version,
		Manifest: application.ReleaseManifest{
			Version:            version,
			Commit:             commit,
			DeploymentContract: 1,
			Images:             map[string]application.ReleaseImage{arch: image},
		},
		Assets: []application.ReleaseAsset{{Name: asset, URL: "https://github.com/acceptance/" + asset, Size: int64(len(contents))}},
	}
}

func acceptanceSaveImage(t *testing.T, ctx context.Context, tag string) string {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), strings.ReplaceAll(tag, ":", "-")+".tar")
	acceptanceDocker(t, ctx, "save", "-o", tarPath, tag)
	source, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	archive := tarPath + ".gz"
	destination, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressor := gzip.NewWriter(destination)
	if _, err := io.Copy(compressor, source); err != nil {
		_ = compressor.Close()
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return archive
}

func acceptanceImageID(t *testing.T, ctx context.Context, tag string) string {
	t.Helper()
	return strings.TrimSpace(acceptanceDockerOutput(t, ctx, "image", "inspect", "--format", "{{.Id}}", tag))
}

func acceptanceAssertTransition(t *testing.T, ctx context.Context, manager, bystander, bystanderID, project, runtimeTag, version, commit string, release application.ReleaseInfo, previousManagerID string, requireData bool) {
	t.Helper()
	acceptanceWaitHealth(t, ctx, manager, version, commit)
	currentManagerID := acceptanceDockerOutput(t, ctx, "inspect", "--format", "{{.Id}}", manager)
	if currentManagerID == previousManagerID {
		t.Fatal("app container was not recreated")
	}
	if got := acceptanceDockerOutput(t, ctx, "inspect", "--format", "{{.Id}}", bystander); got != bystanderID {
		t.Fatal("unrelated container was recreated")
	}
	if got := strings.TrimSpace(acceptanceDockerOutput(t, ctx, "image", "inspect", "--format", "{{.Id}}", runtimeTag)); got != release.Manifest.Images[runtime.GOARCH].ImageID {
		t.Fatalf("runtime image tag resolves to %s, want %s", got, release.Manifest.Images[runtime.GOARCH].ImageID)
	}
	if requireData {
		if got := acceptanceDockerOutput(t, ctx, "exec", manager, "cat", "/data/marker"); got != "acceptance-data" {
			t.Fatalf("persistent data marker = %q", got)
		}
		acceptanceDocker(t, ctx, "exec", manager, "test", "-s", "/data/redlaunch.db")
		if got := acceptanceDockerOutput(t, ctx, "exec", bystander, "cat", "/work/marker"); got != "bystander-data" {
			t.Fatalf("unrelated container data marker = %q", got)
		}
	}
	names := strings.Fields(acceptanceDockerOutput(t, ctx, "ps", "-a", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Names}}"))
	if len(names) != 1 || names[0] != manager {
		t.Fatalf("app project containers = %v, want only %s", names, manager)
	}
}

func acceptanceWaitTerminal(t *testing.T, ctx context.Context, updater *UpdateService, operationID string) application.UpdateState {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		state, ok, err := updater.UpdateState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok && state.OperationID == operationID && state.Terminal() {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not reach a terminal state", operationID)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func acceptanceRequireComplete(t *testing.T, state application.UpdateState) {
	t.Helper()
	if state.Phase != application.UpdatePhaseComplete {
		t.Fatalf("update phase = %s, want complete (error %q)", state.Phase, state.Error)
	}
}

func acceptanceWaitHealth(t *testing.T, ctx context.Context, manager, version, commit string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		output, err := acceptanceDockerRun(ctx, "exec", manager, "/usr/local/bin/redlaunch", "healthcheck", "--url", "http://127.0.0.1:8080/healthz")
		if err == nil {
			var health healthResponse
			if json.Unmarshal([]byte(output), &health) == nil && health.Status == "ok" && health.Version == version && health.Commit == commit {
				return
			}
		}
		if time.Now().After(deadline) {
			logs, _ := acceptanceDockerRun(ctx, "logs", "--tail", "80", manager)
			t.Fatalf("container %s did not report %s/%s: %v %s\n%s", manager, version, commit, err, output, logs)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func acceptanceWaitContainerState(t *testing.T, ctx context.Context, name string, running bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		output, err := acceptanceDockerRun(ctx, "inspect", "--format", "{{.State.Running}}", name)
		if err == nil && strings.TrimSpace(output) == strconv.FormatBool(running) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s did not reach running=%t: %v %s", name, running, err, output)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func acceptanceWaitHelperRunning(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		output, err := acceptanceDockerRun(ctx, "inspect", "--format", "{{.State.Running}}", updateHelperName)
		if err == nil && strings.TrimSpace(output) == "true" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("update helper did not start: %v %s", err, output)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func acceptanceDocker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	output, err := acceptanceDockerRun(ctx, args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func acceptanceDockerOutput(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	return acceptanceDocker(t, ctx, args...)
}

// acceptanceDockerRun is the error-returning variant used by polling loops.
func acceptanceDockerRun(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	return string(output), err
}
