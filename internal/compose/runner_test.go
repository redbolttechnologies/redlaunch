package compose

import (
	"context"
	"os"
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
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\n"), 0o700); err != nil {
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
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\n"), 0o700); err != nil {
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

func TestCommandRunnerServiceActionsUseExplicitComposeArguments(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\n"), 0o700); err != nil {
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
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\n"), 0o700); err != nil {
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

func TestCommandRunnerReloadProxyUsesCaddyComposeExec(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${0%/*}/args\"\n"), 0o700); err != nil {
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
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_SESSION_SECRET", "manager-only-marker")
	t.Setenv("APP_INTERPOLATION_VALUE", "application-marker")

	binary := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
printf '{"services":{"web":{"environment":{"APP_VALUE":"%s","MANAGER_VALUE":"%s"}}}}\n' "${APP_INTERPOLATION_VALUE-unset}" "${AUTH_SESSION_SECRET-unset}"
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
	expected := []string{"compose", "--project-name", composeProjectName(projectDir), "-f", "compose.yml"}
	return append(expected, args...)
}
