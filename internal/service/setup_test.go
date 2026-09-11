package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	directories []string
	services    []string
	err         error
}

func (r *recordingRunner) Up(_ context.Context, projectDir string) error {
	r.directories = append(r.directories, projectDir)
	return r.err
}

func (r *recordingRunner) UpService(_ context.Context, projectDir, serviceName string) error {
	r.directories = append(r.directories, projectDir)
	r.services = append(r.services, serviceName)
	return r.err
}

func TestInitializeCreatesManagedDirectoriesWithExpectedPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	setup, err := NewSetupService(root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.Initialize(); err != nil {
		t.Fatal(err)
	}

	for _, directory := range []string{root, filepath.Join(root, applicationsDir), filepath.Join(root, coreDir)} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat %s: %v", directory, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", directory)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("permissions for %s = %o, want %o", directory, got, 0o755)
		}
	}

	needsSetup, err := setup.NeedsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if !needsSetup {
		t.Fatal("NeedsSetup() = false before setup completion, want true")
	}
}

func TestSetupWritesAndStartsSelectedCoreServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &recordingRunner{}
	setup, err := NewSetupService(root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Initialize(); err != nil {
		t.Fatal(err)
	}

	if err := setup.Setup(context.Background(), true, true); err != nil {
		t.Fatal(err)
	}

	wantDirectories := []string{
		filepath.Join(root, coreDir, proxyDir),
		filepath.Join(root, coreDir, registryDir),
	}
	if len(runner.directories) != len(wantDirectories) {
		t.Fatalf("Compose projects started = %v, want %v", runner.directories, wantDirectories)
	}
	for i := range wantDirectories {
		if runner.directories[i] != wantDirectories[i] {
			t.Errorf("Compose project %d = %q, want %q", i, runner.directories[i], wantDirectories[i])
		}
	}

	proxyComposePath := filepath.Join(root, coreDir, proxyDir, "compose.yml")
	proxyCompose := readTestFile(t, proxyComposePath)
	for _, expected := range []string{
		"image: caddy:2.11.4-alpine",
		"container_name: redbolt-proxy",
		"- vars.env",
		"- secrets.env",
		"host.docker.internal:host-gateway",
		"./Caddyfile:/etc/caddy/Caddyfile:ro",
		"redlaunch.managed=true",
		"name: redlaunch-common",
		"- redlaunch-common",
	} {
		if !strings.Contains(proxyCompose, expected) {
			t.Errorf("proxy Compose file does not contain %q:\n%s", expected, proxyCompose)
		}
	}
	if got := filePermissions(t, proxyComposePath); got != 0o644 {
		t.Errorf("proxy Compose permissions = %o, want %o", got, 0o644)
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		path := filepath.Join(root, coreDir, proxyDir, name)
		if got := readTestFile(t, path); got != "" {
			t.Errorf("proxy %s = %q, want an empty file", name, got)
		}
		if got := filePermissions(t, path); got != envFileMode {
			t.Errorf("proxy %s permissions = %o, want %o", name, got, envFileMode)
		}
	}
	proxyCaddyfilePath := filepath.Join(root, coreDir, proxyDir, "Caddyfile")
	if got := readTestFile(t, proxyCaddyfilePath); got != "# Routes managed by Redlaunch.\n" {
		t.Fatalf("proxy Caddyfile = %q, want the managed header", got)
	}
	if got := filePermissions(t, proxyCaddyfilePath); got != 0o644 {
		t.Fatalf("proxy Caddyfile permissions = %o, want %o", got, 0o644)
	}

	registryComposePath := filepath.Join(root, coreDir, registryDir, "compose.yml")
	registryCompose := readTestFile(t, registryComposePath)
	for _, expected := range []string{
		"image: registry:3.1.1",
		"container_name: redbolt-registry",
		"- vars.env",
		"- secrets.env",
		"127.0.0.1:5000:5000",
		"redlaunch.managed=true",
		"name: redlaunch-registry",
		"- redlaunch-registry",
	} {
		if !strings.Contains(registryCompose, expected) {
			t.Errorf("registry Compose file does not contain %q:\n%s", expected, registryCompose)
		}
	}
	for _, name := range []string{varsEnvFile, secretsEnvFile} {
		path := filepath.Join(root, coreDir, registryDir, name)
		if got := readTestFile(t, path); got != "" {
			t.Errorf("registry %s = %q, want an empty file", name, got)
		}
		if got := filePermissions(t, path); got != envFileMode {
			t.Errorf("registry %s permissions = %o, want %o", name, got, envFileMode)
		}
	}

	needsSetup, err := setup.NeedsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if needsSetup {
		t.Fatal("NeedsSetup() = true after successful setup, want false")
	}
	if got := readTestFile(t, filepath.Join(root, setupMarker)); got != "complete\n" {
		t.Fatalf("setup marker = %q, want %q", got, "complete\n")
	}
}

func TestSetupCanCompleteWithoutOptionalServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &recordingRunner{}
	setup, err := NewSetupService(root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.Setup(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if len(runner.directories) != 0 {
		t.Fatalf("Compose projects started = %v, want none", runner.directories)
	}
	needsSetup, err := setup.NeedsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if needsSetup {
		t.Fatal("NeedsSetup() = true after completing without services, want false")
	}
}

func TestEnsureRegistryInstallsWithoutCompletingFirstRunSetup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &recordingRunner{}
	setup, err := NewSetupService(root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.EnsureRegistry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := setup.EnsureRegistry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runner.directories) != 2 {
		t.Fatalf("registry starts = %v, want two starts", runner.directories)
	}
	wantDirectory := filepath.Join(root, coreDir, registryDir)
	for _, directory := range runner.directories {
		if directory != wantDirectory {
			t.Errorf("registry directory = %q, want %q", directory, wantDirectory)
		}
	}
	for _, name := range []string{"compose.yml", varsEnvFile, secretsEnvFile} {
		if _, err := os.Stat(filepath.Join(wantDirectory, name)); err != nil {
			t.Errorf("registry file %s: %v", name, err)
		}
	}
	needsSetup, err := setup.NeedsSetup()
	if err != nil {
		t.Fatal(err)
	}
	if !needsSetup {
		t.Fatal("EnsureRegistry() completed first-run setup unexpectedly")
	}
}

func TestSetupReportsProgressStages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	setup, err := NewSetupService(root, &recordingRunner{})
	if err != nil {
		t.Fatal(err)
	}

	var stages []string
	if err := setup.SetupWithProgress(context.Background(), false, false, func(stage, _ string) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"directories", "finalize"}
	if len(stages) != len(want) {
		t.Fatalf("progress stages = %v, want %v", stages, want)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Errorf("progress stage %d = %q, want %q", i, stages[i], want[i])
		}
	}
}

func TestSetupDoesNotWriteCompletionMarkerWhenComposeFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	runner := &recordingRunner{err: errors.New("compose unavailable")}
	setup, err := NewSetupService(root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := setup.Setup(context.Background(), true, false); err == nil {
		t.Fatal("Setup() error = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(root, setupMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup marker stat error = %v, want not exist", err)
	}
}

func TestNewSetupServiceRejectsFilesystemRoot(t *testing.T) {
	if _, err := NewSetupService(string(filepath.Separator), &recordingRunner{}); err == nil {
		t.Fatal("NewSetupService() error = nil, want error")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func filePermissions(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
