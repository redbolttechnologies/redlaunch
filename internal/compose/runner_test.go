package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindComposeFilePrefersApplicationComposeYML(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"compose.yml", "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := findComposeFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got != "compose.yml" {
		t.Fatalf("findComposeFile() = %q, want compose.yml", got)
	}
}

func TestFindComposeFileFallsBackToComposeYAML(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findComposeFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got != "compose.yaml" {
		t.Fatalf("findComposeFile() = %q, want compose.yaml", got)
	}
}

func TestCommandRunnerUpServiceUsesExplicitComposeArguments(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).UpService(context.Background(), projectDir, "db"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "up", "-d", "db")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Compose arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerRestartProjectBuildsRecreatesAndWaits(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).RestartProject(context.Background(), projectDir); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "up", "-d", "--build", "--force-recreate", "--wait", "--wait-timeout", "30")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Compose arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerConfigServicesUsesMachineReadableComposeConfiguration(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf '%s\\n' '{\"services\":{\"web\":{\"image\":\"nginx:1.27\"},\"db\":{\"image\":\"postgres:17\"}}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	services, err := (CommandRunner{Binary: binary}).ConfigServices(context.Background(), projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0] != (ConfiguredService{Name: "db", Image: "postgres:17"}) || services[1] != (ConfiguredService{Name: "web", Image: "nginx:1.27"}) {
		t.Fatalf("ConfigServices() = %#v, want sorted configured services", services)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "config", "--format", "json", "--no-interpolate", "--no-env-resolution", "--no-path-resolution")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Compose arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerEnsureNetworkCreatesManagedNetwork(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"${0%/*}/args\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).EnsureNetwork(context.Background(), "redlaunch-common"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(contents)), "\n")
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "network\x00create") || !strings.Contains(joined, "--label\x00redlaunch.managed=true") || !strings.Contains(joined, "--label\x00redlaunch.owner=redlaunch") || !strings.HasSuffix(joined, "redlaunch-common") {
		t.Fatalf("EnsureNetwork() arguments = %#v, want managed network creation", args)
	}
}

func TestCommandRunnerEnsureNetworkAcceptsOnlyOwnedExistingNetwork(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		network string
		wantErr bool
	}{
		{name: "owned", network: `{"Driver":"bridge","Scope":"local","Internal":false,"Labels":{"redlaunch.managed":"true","redlaunch.owner":"redlaunch"}}`},
		{name: "legacy compose-created", network: `{"Driver":"bridge","Scope":"local","Internal":false,"Labels":{"com.docker.compose.network":"redlaunch-common","com.docker.compose.project":"proxy"}}`},
		{name: "legacy manual", network: `{"Driver":"bridge","Scope":"local","Internal":false,"Labels":null}`},
		{name: "foreign owner", network: `{"Driver":"bridge","Scope":"local","Internal":false,"Labels":{"redlaunch.owner":"someone-else"}}`, wantErr: true},
		{name: "wrong driver", network: `{"Driver":"overlay","Scope":"swarm","Internal":false,"Labels":null}`, wantErr: true},
		{name: "internal", network: `{"Driver":"bridge","Scope":"local","Internal":true,"Labels":null}`, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "docker")
			script := "#!/bin/sh\nif [ \"$1\" = network ] && [ \"$2\" = ls ]; then printf '%s\\n' redlaunch-common; exit 0; fi\nif [ \"$1\" = network ] && [ \"$2\" = inspect ]; then printf '%s\\n' '" + testCase.network + "'; exit 0; fi\nexit 1\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := (CommandRunner{Binary: binary}).EnsureNetwork(context.Background(), "redlaunch-common")
			if (err != nil) != testCase.wantErr {
				t.Fatalf("EnsureNetwork() error = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestCommandRunnerEnsureNetworkRejectsInvalidName(t *testing.T) {
	if err := (CommandRunner{Binary: filepath.Join(t.TempDir(), "missing")}).EnsureNetwork(context.Background(), "../outside"); err == nil {
		t.Fatal("EnsureNetwork() error = nil, want invalid network name")
	}
}

func TestCommandRunnerServiceActionsUseExplicitComposeArguments(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	runner := CommandRunner{Binary: binary}
	for _, testCase := range []struct {
		name    string
		action  func(context.Context, string, string) error
		command string
	}{
		{name: "start", action: runner.Start, command: "start"},
		{name: "stop", action: runner.Stop, command: "stop"},
		{name: "restart", action: runner.Restart, command: "restart"},
		{name: "remove", action: runner.Remove, command: "rm -f"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.action(context.Background(), projectDir, "db"); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSpace(string(contents)), "\n")
			want := expectedComposeArguments(projectDir)
			want = append(want, strings.Split(testCase.command, " ")...)
			want = append(want, "db")
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("Compose arguments = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCommandRunnerDownRemovesProjectContainersAndResources(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "down", "--volumes", "--remove-orphans")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Down() arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerRefusesDestructiveActionForUnmanagedContainer(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
case "$1" in
  ps) printf 'container-id\n' ;;
  inspect) printf '{"com.docker.compose.project":"wrong-project"}\n' ;;
  compose) printf '{}\n' ;;
  *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err == nil {
		t.Fatal("Down() returned nil error for an unmanaged container")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "compose-args")); !os.IsNotExist(err) {
		t.Fatalf("Compose command marker stat error = %v, want no destructive Compose command", err)
	}
}

func TestCommandRunnerRefusesDestructiveActionForUnownedVolume(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
case "$1:$2" in
  ps:*) ;;
  volume:ls) printf 'volume-name\n' ;;
  volume:inspect) printf '{"com.docker.compose.project":"wrong-project"}\n' ;;
  compose:*) printf '{}\n' ;;
  *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err == nil {
		t.Fatal("Down() returned nil error for an unowned volume")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "compose-args")); !os.IsNotExist(err) {
		t.Fatalf("Compose command marker stat error = %v, want no destructive Compose command", err)
	}
}

func TestCommandRunnerAllowsDestructiveActionForManagedProject(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectName := composeProjectName(projectDir)
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$1\" in\n  ps) printf 'container-id\\n' ;;\n  inspect) printf '{\"redlaunch.managed\":\"true\",\"com.docker.compose.project\":\"" + projectName + "\"}\\n' ;;\n  compose) printf '{}\\n' ;;\n  *) printf '%s\\n' \"$@\" > \"${0%/*}/compose-args\" ;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "compose-args")); err != nil {
		t.Fatalf("Compose command marker stat error = %v, want destructive Compose command", err)
	}
}

func TestConfiguredNonExternalVolumeNamesResolvesDefaults(t *testing.T) {
	output := []byte(`{"volumes":{"reviewdata":{"name":"foreign-vol"},"scoped":{},"ext":{"external":true,"name":"keep-me"},"extobj":{"external":{"name":"keep-too"}}}}`)
	got, err := configuredNonExternalVolumeNames(output, "myproject")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"foreign-vol", "myproject_scoped"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("configuredNonExternalVolumeNames() = %v, want %v", got, want)
	}
	for _, raw := range []string{`true`, `{"name":"x"}`} {
		if !isExternalVolume([]byte(raw)) {
			t.Fatalf("isExternalVolume(%s) = false, want true", raw)
		}
	}
	for _, raw := range []string{``, `null`, `false`} {
		if isExternalVolume([]byte(raw)) {
			t.Fatalf("isExternalVolume(%q) = true, want false", raw)
		}
	}
}

func TestCommandRunnerRefusesDestructiveActionForForeignConfiguredVolume(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
case "$1:$2" in
  ps:*) ;;
  volume:ls)
    case "$4" in
      *label=com.docker.compose.project=*) ;;
      *foreign-vol*) printf 'foreign-vol\n' ;;
    esac ;;
  volume:inspect) printf '{"com.docker.compose.project":"other-project"}\n' ;;
  compose:*) case " $* " in *" config "*) printf '{"volumes":{"reviewdata":{"name":"foreign-vol"}}}\n' ;; *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;; esac ;;
  *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err == nil {
		t.Fatal("Down() returned nil error for a foreign configured volume")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "compose-args")); !os.IsNotExist(err) {
		t.Fatalf("Compose command marker stat error = %v, want no destructive Compose command", err)
	}
}

func TestCommandRunnerAllowsDestructiveActionWhenForeignVolumeNameIsAbsent(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
case "$1:$2" in
  ps:*) ;;
  volume:ls) ;;
  compose:*) case " $* " in *" config "*) printf '{"volumes":{"reviewdata":{"name":"not-yet-created"}}}\n' ;; *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;; esac ;;
  *) printf '%s\n' "$@" > "${0%/*}/compose-args" ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := (CommandRunner{Binary: binary}).Down(context.Background(), projectDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "compose-args")); err != nil {
		t.Fatalf("Compose command marker stat error = %v, want destructive Compose command", err)
	}
}

// TestConfiguredVolumeNamesMatchRealComposeResolution resolves the R01
// synthetic foreign-volume input with the real Compose binary (no containers,
// volumes, or networks are created) and verifies the ownership parser sees
// the foreign name. It skips when docker is unavailable.
func TestConfiguredVolumeNamesMatchRealComposeResolution(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("docker is not installed: %v", err)
	}
	projectDir := t.TempDir()
	contents := "services:\n  web:\n    image: busybox:1.36\n    volumes: [reviewdata:/data]\nvolumes:\n  reviewdata: {name: redlaunch-review-foreign}\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(docker, "compose", "--project-name", composeProjectName(projectDir), "--env-file", "/dev/null", "-f", "compose.yml", "config", "--format", "json")
	command.Dir = projectDir
	command.Env = composeProcessEnvironment(os.Environ(), nil)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve synthetic Compose configuration: %v", err)
	}
	names, err := configuredNonExternalVolumeNames(output, composeProjectName(projectDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "redlaunch-review-foreign" {
		t.Fatalf("configured volume names = %v, want [redlaunch-review-foreign]", names)
	}
}

func TestCommandRunnerReloadProxyUsesCaddyComposeExec(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\ncase \" $* \" in *\" config \"*) printf '{}\\n';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (CommandRunner{Binary: binary}).ReloadProxy(context.Background(), projectDir); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "exec", "-T", "proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("ReloadProxy() arguments = %#v, want %#v", got, want)
	}
}

func TestDecodeServiceRuntimesReadsDockerComposeJSONLines(t *testing.T) {
	output := []byte("{\"Service\":\"db\",\"Name\":\"redbolt-7-db\",\"CreatedAt\":\"2026-08-30 07:25:29 +0200 CEST\",\"Status\":\"Up 9 minutes\",\"Image\":\"postgres:16\",\"Ports\":\"5432/tcp\"}\n" +
		"{\"Service\":\"web\",\"Names\":\"redbolt-7-web\",\"State\":\"exited\",\"Ports\":\"\"}")

	got, err := decodeServiceRuntimes(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decodeServiceRuntimes() returned %d rows, want 2", len(got))
	}
	if got[0].ServiceName != "db" || got[0].ContainerName != "redbolt-7-db" || got[0].Status != "Up 9 minutes" || got[0].Image != "postgres:16" || got[0].Ports != "5432/tcp" || !got[0].Running {
		t.Fatalf("decoded database runtime = %#v", got[0])
	}
	wantCreatedAt := time.Date(2026, time.August, 30, 7, 25, 29, 0, time.FixedZone("CEST", 2*60*60))
	if !got[0].CreatedAt.Equal(wantCreatedAt) {
		t.Fatalf("decoded CreatedAt = %s, want %s", got[0].CreatedAt, wantCreatedAt)
	}
	if got[1].ContainerName != "redbolt-7-web" || got[1].Status != "exited" || got[1].Running {
		t.Fatalf("decoded fallback runtime = %#v", got[1])
	}
}

func TestDecodeServiceRuntimesAcceptsJSONArray(t *testing.T) {
	got, err := decodeServiceRuntimes([]byte(`[{"Service":"cache","Name":"redbolt-7-cache","Status":"Up","Ports":"6379/tcp"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ServiceName != "cache" || got[0].ContainerName != "redbolt-7-cache" || !got[0].Running {
		t.Fatalf("decoded JSON array = %#v, want cache runtime", got)
	}
}

func TestCommandRunnerIsServiceRunningReadsComposeRuntimeState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		state   string
		running bool
	}{
		{name: "running", state: "running", running: true},
		{name: "stopped", state: "exited", running: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "docker")
			script := "#!/bin/sh\nprintf '%s\\n' '[{\"Service\":\"db\",\"State\":\"" + testCase.state + "\"}]'\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			running, err := (CommandRunner{Binary: binary}).IsServiceRunning(context.Background(), projectDir, "db")
			if err != nil {
				t.Fatal(err)
			}
			if running != testCase.running {
				t.Fatalf("IsServiceRunning() = %v, want %v", running, testCase.running)
			}
		})
	}
}

func TestCommandRunnerLogsUsesBoundedTailAndNoColor(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf 'line one\\nline two\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	logs, err := (CommandRunner{Binary: binary}).Logs(context.Background(), projectDir, "db", 25)
	if err != nil {
		t.Fatal(err)
	}
	if logs != "line one\nline two\n" {
		t.Fatalf("Logs() = %q, want command output", logs)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "logs", "--tail", "25", "--no-color", "--no-log-prefix", "db")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Logs() arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerAllLogsDoesNotApplyTail(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf 'old line\\nnew line\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	logs, err := (CommandRunner{Binary: binary}).AllLogs(context.Background(), projectDir, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if logs != "old line\nnew line\n" {
		t.Fatalf("AllLogs() = %q, want command output", logs)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "logs", "--no-color", "--no-log-prefix", "proxy")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("AllLogs() arguments = %#v, want %#v", got, want)
	}
}

func TestCommandOutputBuffersAreBoundedAtWriteTime(t *testing.T) {
	contents := make([]byte, maxCommandOutput*4)
	tail := newTailBuffer(maxCommandOutput)
	if written, err := tail.Write(contents); err != nil || written != len(contents) {
		t.Fatalf("tail Write() = (%d, %v), want all input accepted", written, err)
	}
	if len(tail.Bytes()) != maxCommandOutput || !tail.truncated {
		t.Fatalf("tail buffer = (%d bytes, truncated=%v), want %d-byte bounded tail", len(tail.Bytes()), tail.truncated, maxCommandOutput)
	}

	structured := newBoundedBuffer(maxCommandOutput)
	if written, err := structured.Write(contents); err != nil || written != len(contents) {
		t.Fatalf("structured Write() = (%d, %v), want all input accepted", written, err)
	}
	if len(structured.Bytes()) != maxCommandOutput || !structured.truncated {
		t.Fatalf("structured buffer = (%d bytes, truncated=%v), want %d-byte bounded output", len(structured.Bytes()), structured.truncated, maxCommandOutput)
	}
}

func TestLogStreamAdmissionIsBoundedAndCancellable(t *testing.T) {
	releases := make([]func(), 0, maxConcurrentLogStreams)
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	for index := 0; index < maxConcurrentLogStreams; index++ {
		release, err := acquireLogStream(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireLogStream(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended log stream acquire error = %v, want context canceled", err)
	}

	for _, release := range releases {
		release()
	}
	releases = nil
	release, err := acquireLogStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestCommandRunnerRejectsOversizedStructuredOutput(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nhead -c 5000000 /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := (CommandRunner{Binary: binary}).ConfigServices(context.Background(), projectDir)
	if err == nil || !strings.Contains(err.Error(), "structured output exceeds") {
		t.Fatalf("ConfigServices() error = %v, want bounded structured-output error", err)
	}
}

func TestCommandDiagnosticsKeepOnlyRedactedTail(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "secrets.env"), []byte("DATABASE_PASSWORD=super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "db.secrets.env"), []byte("SCOPED_TOKEN=scoped-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "diagnostic")
	script := "#!/bin/sh\nhead -c 100000 /dev/zero >&2\nprintf ' DATABASE_PASSWORD=super-secret SCOPED_TOKEN=scoped-secret\\n' >&2\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	err := runDiagnosticCommand(exec.Command(binary), "diagnostic command", projectDir)
	if err == nil {
		t.Fatal("runDiagnosticCommand() error = nil, want command failure")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "scoped-secret") {
		t.Fatalf("diagnostic error leaked secret: %v", err)
	}
	if len(err.Error()) > maxCommandOutput+128 {
		t.Fatalf("diagnostic error length = %d, want bounded tail", len(err.Error()))
	}
}

func TestCommandRunnerOpenLogsStreamsAndStopsAtLimit(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '12345'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	stream, err := (CommandRunner{Binary: binary, MaxLogBytes: 4}).OpenLogs(context.Background(), projectDir, "db")
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if string(contents) != "1234" {
		t.Fatalf("streamed log contents = %q, want 4-byte prefix", contents)
	}
	if !errors.Is(readErr, ErrLogOutputTooLarge) {
		t.Fatalf("stream read error = %v, want ErrLogOutputTooLarge", readErr)
	}
	if closeErr != nil && !errors.Is(closeErr, ErrLogOutputTooLarge) {
		t.Fatalf("stream close error = %v, want nil or size error", closeErr)
	}
}

func TestCommandRunnerEnvironmentReadsResolvedComposeConfig(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\nprintf '{\"services\":{\"db\":{\"environment\":{\"POSTGRES_DB\":\"status\",\"POSTGRES_PASSWORD\":\"secret\"}}}}\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	environment, err := (CommandRunner{Binary: binary}).Environment(context.Background(), projectDir, "db")
	if err != nil {
		t.Fatal(err)
	}
	if len(environment) != 2 || environment[0] != (EnvironmentVariable{Key: "POSTGRES_DB", Value: "status"}) || environment[1] != (EnvironmentVariable{Key: "POSTGRES_PASSWORD", Value: "secret"}) {
		t.Fatalf("Environment() = %#v, want sorted resolved variables", environment)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir, "config", "--format", "json", "db")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Environment() arguments = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerEnvironmentUsesComposeResolutionForSyntheticEnvFiles(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI is not installed")
	}
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services:\n  web:\n    image: alpine:3.22\n    env_file:\n      - vars.env\n      - secrets.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "vars.env"), []byte("LITERAL_DOLLAR=$$5\nINTERPOLATED=${MISSING_VALUE:-fallback}\nQUOTED=\"quoted value\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "secrets.env"), []byte("SYNTHETIC_SECRET=not-a-real-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	values, err := (CommandRunner{}).Environment(context.Background(), projectDir, "web")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(values))
	for _, value := range values {
		got[value.Key] = value.Value
	}
	for key, want := range map[string]string{
		"LITERAL_DOLLAR":   "$$5",
		"INTERPOLATED":     "fallback",
		"QUOTED":           "quoted value",
		"SYNTHETIC_SECRET": "not-a-real-secret",
	} {
		if got[key] != want {
			t.Fatalf("Compose-resolved %s = %q, want %q; all values = %#v", key, got[key], want, got)
		}
	}
}

func TestComposeProjectNamesSeparateApplicationAndCoreResources(t *testing.T) {
	root := t.TempDir()
	applicationDir := filepath.Join(root, "applications", "proxy")
	coreDir := filepath.Join(root, "core", "proxy")

	applicationProject := composeProjectName(applicationDir)
	coreProject := composeProjectName(coreDir)
	if applicationProject == coreProject {
		t.Fatalf("application and core Compose project names both equal %q", applicationProject)
	}
	if !strings.HasPrefix(applicationProject, "redlaunch-app-proxy-") {
		t.Fatalf("application Compose project name = %q, want redlaunch app prefix", applicationProject)
	}
	if !strings.HasPrefix(coreProject, "redlaunch-core-proxy-") {
		t.Fatalf("core Compose project name = %q, want redlaunch core prefix", coreProject)
	}
}

func TestCommandRunnerExcludesManagerOnlyEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services:\n  web:\n    environment:\n      APP_VALUE: ${APP_INTERPOLATION_VALUE}\n      MANAGER_VALUE: ${AUTH_SESSION_SECRET}\n      GOOGLE_VALUE: ${GOOGLE_CLIENT_SECRET}\n      METRICS_SCOPE_VALUE: ${METRICS_SCOPE}\n      METRICS_PROC_VALUE: ${METRICS_PROC_ROOT}\n      METRICS_FILESYSTEM_VALUE: ${METRICS_FILESYSTEM_ROOT}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_SESSION_SECRET", "manager-only-marker")
	t.Setenv("GOOGLE_CLIENT_SECRET", "manager-google-marker")
	t.Setenv("METRICS_SCOPE", "vps")
	t.Setenv("METRICS_PROC_ROOT", "/host/proc")
	t.Setenv("METRICS_FILESYSTEM_ROOT", "/host/root")
	t.Setenv("APP_INTERPOLATION_VALUE", "application-marker")

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
printf '{"services":{"web":{"environment":{"APP_VALUE":"%s","MANAGER_VALUE":"%s","GOOGLE_VALUE":"%s","METRICS_SCOPE_VALUE":"%s","METRICS_PROC_VALUE":"%s","METRICS_FILESYSTEM_VALUE":"%s"}}}}\n' "${APP_INTERPOLATION_VALUE-unset}" "${AUTH_SESSION_SECRET-unset}" "${GOOGLE_CLIENT_SECRET-unset}" "${METRICS_SCOPE-unset}" "${METRICS_PROC_ROOT-unset}" "${METRICS_FILESYSTEM_ROOT-unset}"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	environment, err := (CommandRunner{Binary: binary}).Environment(context.Background(), projectDir, "web")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment))
	for _, variable := range environment {
		values[variable.Key] = variable.Value
	}
	if values["MANAGER_VALUE"] != "unset" {
		t.Fatal("manager-only authentication environment reached Docker Compose")
	}
	if values["GOOGLE_VALUE"] != "unset" {
		t.Fatal("manager-only OAuth environment reached Docker Compose")
	}
	for _, key := range []string{"METRICS_SCOPE_VALUE", "METRICS_PROC_VALUE", "METRICS_FILESYSTEM_VALUE"} {
		if values[key] != "unset" {
			t.Fatalf("manager-only metrics environment %s reached Docker Compose", key)
		}
	}
	if values["APP_VALUE"] != "application-marker" {
		t.Fatalf("application interpolation environment = %q, want application-marker", values["APP_VALUE"])
	}
}

func TestDecodeServiceEnvironmentSupportsEmptyAndScalarValues(t *testing.T) {
	got, err := decodeServiceEnvironment([]byte(`{"services":{"db":{"environment":{"COUNT":17,"EMPTY":null,"POSTGRES_DB":"status"}}}}`), "db")
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvironmentVariable{
		{Key: "COUNT", Value: "17"},
		{Key: "EMPTY", Value: ""},
		{Key: "POSTGRES_DB", Value: "status"},
	}
	if strings.Join(environmentPairs(got), "\x00") != strings.Join(environmentPairs(want), "\x00") {
		t.Fatalf("decodeServiceEnvironment() = %#v, want %#v", got, want)
	}
}

func environmentPairs(values []EnvironmentVariable) []string {
	pairs := make([]string, 0, len(values))
	for _, value := range values {
		pairs = append(pairs, value.Key+"="+value.Value)
	}
	return pairs
}

func expectedComposeArguments(projectDir string, args ...string) []string {
	expected := []string{"compose", "--project-name", composeProjectName(projectDir), "--env-file", "/dev/null", "-f", "compose.yml"}
	return append(expected, args...)
}

func TestPostgresRestoreRunsInsideASingleTransaction(t *testing.T) {
	if !strings.Contains(postgresRestoreScript, "--single-transaction") {
		t.Fatalf("postgres restore script = %q, want --single-transaction for atomic restores", postgresRestoreScript)
	}
	if !strings.Contains(postgresRestoreScript, "ON_ERROR_STOP=1") {
		t.Fatalf("postgres restore script = %q, want ON_ERROR_STOP=1 preserved", postgresRestoreScript)
	}
	if !strings.Contains(postgresDumpScript, "--clean") || !strings.Contains(postgresDumpScript, "--if-exists") {
		t.Fatalf("postgres dump script = %q, want --clean --if-exists preserved", postgresDumpScript)
	}
}
