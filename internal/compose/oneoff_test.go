package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeOneOffDocker(t *testing.T, script string) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return projectDir, binary
}

func TestRunOneOffBuildsExplicitArgvWithOrderedArgumentsAndEnvironment(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, `#!/bin/sh
printf '%s\n' "$@" > "${0%/*}/args"
case " $* " in
  *" config "*) printf '{}\n' ;;
  *" run "*) printf 'stdout line\n'; printf 'stderr line\n' >&2 ;;
esac
`)
	parameters := OneOffRunParameters{
		Args: []string{"--database-name", "example name", "$(touch injected)", "--confirm"},
		Env:  map[string]string{"NODE_ENV": "development", "TZ": "UTC"},
	}
	result, err := (CommandRunner{Binary: binary}).RunOneOff(context.Background(), projectDir, "migrate", parameters)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "stdout line\n" || result.Stderr != "stderr line\n" || result.Truncated {
		t.Fatalf("RunOneOff() result = %+v, want captured success", result)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(contents)), "\n")
	want := expectedComposeArguments(projectDir,
		"--ansi", "never",
		"run", "--rm", "--quiet-pull", "-T",
		"--env", "NODE_ENV=development",
		"--env", "TZ=UTC",
		"migrate",
		"--database-name", "example name", "$(touch injected)", "--confirm",
	)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Compose arguments = %#v, want %#v", got, want)
	}
	for _, argument := range got {
		if argument == "--entrypoint" {
			t.Fatal("one-off run attempted an entrypoint override")
		}
	}
}

func TestRunOneOffValidatesParametersBeforeInvokingCompose(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, "#!/bin/sh\nexit 0\n")
	_, err := (CommandRunner{Binary: binary}).RunOneOff(context.Background(), projectDir, "migrate", OneOffRunParameters{
		Env: map[string]string{"PATH": "/tmp"},
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("RunOneOff() error = %v, want environment allowlist rejection", err)
	}
}

func TestRunOneOffReturnsNonZeroExitAndSanitizedError(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, `#!/bin/sh
case " $* " in
  *" config "*) printf '{}\n' ;;
  *" run "*)
    printf 'before failure\n'
    printf 'DATABASE_PASSWORD=super-secret SCOPED_TOKEN=scoped-secret\n' >&2
    exit 7
    ;;
esac
`)
	if err := os.WriteFile(filepath.Join(projectDir, "secrets.env"), []byte("DATABASE_PASSWORD=super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "migrate.secrets.env"), []byte("SCOPED_TOKEN=scoped-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (CommandRunner{Binary: binary}).RunOneOff(context.Background(), projectDir, "migrate", OneOffRunParameters{})
	if err == nil {
		t.Fatal("RunOneOff() error = nil, want non-zero exit error")
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
	if result.Stdout != "before failure\n" {
		t.Fatalf("stdout = %q, want partial output", result.Stdout)
	}
	if strings.Contains(result.Stderr, "super-secret") || strings.Contains(result.Stderr, "scoped-secret") || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "scoped-secret") {
		t.Fatal("captured output or error leaked a secret")
	}
	if !strings.Contains(result.Stderr, "[REDACTED]") {
		t.Fatalf("stderr = %q, want redaction marker", result.Stderr)
	}
}

func TestRunOneOffStripsTerminalSequencesAndInvalidUTF8(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, `#!/bin/sh
case " $* " in
  *" config "*) printf '{}\n' ;;
  *" run "*)
    printf 'plain \033[31mred\033[0m \033]0;window title\007 done\n'
    printf 'invalid \377 bytes\n'
    ;;
esac
`)
	result, err := (CommandRunner{Binary: binary}).RunOneOff(context.Background(), projectDir, "migrate", OneOffRunParameters{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(result.Stdout, 0x1b) || strings.Contains(result.Stdout, "window title") {
		t.Fatalf("stdout retained terminal escapes: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "plain red  done") {
		t.Fatalf("stdout = %q, want stripped text", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "\uFFFD") {
		t.Fatalf("stdout = %q, want invalid UTF-8 replacement", result.Stdout)
	}
}

func TestRunOneOffTruncatesBoundedOutput(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, `#!/bin/sh
case " $* " in
  *" config "*) printf '{}\n' ;;
  *" run "*) yes a | head -c 200000 ;;
esac
`)
	result, err := (CommandRunner{Binary: binary}).RunOneOff(context.Background(), projectDir, "migrate", OneOffRunParameters{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(result.Stdout) > maxOneOffOutputPerStream {
		t.Fatalf("stdout length = %d, want at most %d", len(result.Stdout), maxOneOffOutputPerStream)
	}
}

func TestRunOneOffCapturesPartialOutputOnCancellation(t *testing.T) {
	projectDir, binary := writeOneOffDocker(t, `#!/bin/sh
case " $* " in
  *" config "*) printf '{}\n' ;;
  *" run "*)
    printf 'partial start\n'
    touch "${0%/*}/started"
    exec sleep 10
    ;;
esac
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result OneOffRunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := (CommandRunner{Binary: binary}).RunOneOff(ctx, projectDir, "migrate", OneOffRunParameters{})
		done <- outcome{result: result, err: err}
	}()
	startedPath := filepath.Join(filepath.Dir(binary), "started")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("one-off run did not start in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	got := <-done
	if got.err == nil {
		t.Fatal("RunOneOff() error = nil, want cancellation error")
	}
	if got.result.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1 for a canceled process", got.result.ExitCode)
	}
	if !strings.Contains(got.result.Stdout, "partial start") {
		t.Fatalf("stdout = %q, want partial output", got.result.Stdout)
	}
}

func TestSanitizeTerminalOutputRemovesControlSequences(t *testing.T) {
	input := "a\x1b[1;31mb\x1b[0m\x1b]0;title\x07c\rd\be\x00f\x9b"
	got := sanitizeTerminalOutput(input)
	for _, forbidden := range []string{"\x1b", "\x00", "\r", "\b", "\x9b", "title"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sanitized output %q still contains %q", got, forbidden)
		}
	}
	if !strings.Contains(got, "abc") || strings.Contains(got, "d") || !strings.Contains(got, "e") || !strings.Contains(got, "f") {
		t.Fatalf("sanitized output = %q, want printable text preserved and backspace applied", got)
	}
}
