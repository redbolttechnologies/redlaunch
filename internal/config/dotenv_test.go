package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	values, err := parseDotEnv(strings.NewReader(`# comments and blank lines are ignored
HTTP_ADDR = :9090
export GOOGLE_CLIENT_ID="client id"
GOOGLE_CLIENT_SECRET='secret # value'
AUTH_SESSION_SECRET=secret#suffix
COMMENTED=plain value # inline comment
ESCAPED="line\nnext" # trailing comment
EMPTY=
`))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"HTTP_ADDR":            ":9090",
		"GOOGLE_CLIENT_ID":     "client id",
		"GOOGLE_CLIENT_SECRET": "secret # value",
		"AUTH_SESSION_SECRET":  "secret#suffix",
		"COMMENTED":            "plain value",
		"ESCAPED":              "line\nnext",
		"EMPTY":                "",
	}
	for key, expected := range want {
		if values[key] != expected {
			t.Errorf("dotenv %s = %q, want %q", key, values[key], expected)
		}
	}
	if len(values) != len(want) {
		t.Fatalf("parsed %d dotenv values, want %d: %#v", len(values), len(want), values)
	}
}

func TestParseDotEnvRejectsMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"missing equals":        "GOOGLE_CLIENT_ID\n",
		"invalid key":           "GOOGLE.CLIENT_ID=value\n",
		"single quote":          "GOOGLE_CLIENT_ID='value\n",
		"double quote":          "GOOGLE_CLIENT_ID=\"value\n",
		"quoted suffix":         "GOOGLE_CLIENT_ID=\"value\" extra\n",
		"invalid double escape": "GOOGLE_CLIENT_ID=\"value\\q\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDotEnv(strings.NewReader(input)); err == nil {
				t.Fatal("parseDotEnv() returned nil error")
			}
		})
	}
}

func TestLoadEnvironmentReadsDefaultDotEnvFile(t *testing.T) {
	t.Chdir(t.TempDir())
	unsetEnvironment(t, dotEnvFileEnvironmentVariable)
	unsetEnvironment(t, "REDLAUNCH_DOTENV_TEST_VALUE")
	writeDotEnvTestFile(t, ".env", "REDLAUNCH_DOTENV_TEST_VALUE=from-default-file\n")

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if environment["REDLAUNCH_DOTENV_TEST_VALUE"] != "from-default-file" {
		t.Fatalf("dotenv value = %q, want %q", environment["REDLAUNCH_DOTENV_TEST_VALUE"], "from-default-file")
	}
}

func TestLoadEnvironmentUsesProcessEnvironmentOverDotEnv(t *testing.T) {
	dotEnvPath := filepath.Join(t.TempDir(), "application.env")
	writeDotEnvTestFile(t, dotEnvPath, "REDLAUNCH_DOTENV_TEST_VALUE=from-file\n")
	unsetEnvironment(t, "REDLAUNCH_DOTENV_TEST_VALUE")
	t.Setenv(dotEnvFileEnvironmentVariable, dotEnvPath)
	t.Setenv("REDLAUNCH_DOTENV_TEST_VALUE", "from-process")

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if environment["REDLAUNCH_DOTENV_TEST_VALUE"] != "from-process" {
		t.Fatalf("dotenv precedence value = %q, want %q", environment["REDLAUNCH_DOTENV_TEST_VALUE"], "from-process")
	}
}

func TestLoadEnvironmentPreservesExplicitEmptyProcessEnvironmentValue(t *testing.T) {
	dotEnvPath := filepath.Join(t.TempDir(), "application.env")
	writeDotEnvTestFile(t, dotEnvPath, "REDLAUNCH_DOTENV_TEST_VALUE=from-file\n")
	t.Setenv(dotEnvFileEnvironmentVariable, dotEnvPath)
	t.Setenv("REDLAUNCH_DOTENV_TEST_VALUE", "")

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if environment["REDLAUNCH_DOTENV_TEST_VALUE"] != "" {
		t.Fatalf("dotenv empty-value precedence = %q, want empty value", environment["REDLAUNCH_DOTENV_TEST_VALUE"])
	}
}

func TestLoadEnvironmentCanDisableDotEnvLoading(t *testing.T) {
	dotEnvPath := filepath.Join(t.TempDir(), "application.env")
	writeDotEnvTestFile(t, dotEnvPath, "REDLAUNCH_DOTENV_TEST_VALUE=from-file\n")
	unsetEnvironment(t, "REDLAUNCH_DOTENV_TEST_VALUE")
	t.Setenv(dotEnvFileEnvironmentVariable, "")

	environment, err := loadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := environment["REDLAUNCH_DOTENV_TEST_VALUE"]; exists {
		t.Fatalf("dotenv value loaded while dotenv support was disabled: %q", environment["REDLAUNCH_DOTENV_TEST_VALUE"])
	}
}

func TestLoadEnvironmentRejectsMissingExplicitDotEnvFile(t *testing.T) {
	dotEnvPath := filepath.Join(t.TempDir(), "missing.env")
	t.Setenv(dotEnvFileEnvironmentVariable, dotEnvPath)

	if _, err := loadEnvironment(); err == nil {
		t.Fatal("loadEnvironment() returned nil error for a missing explicit dotenv file")
	}
}

func TestLoadReadsGoogleAuthenticationSettingsFromDotEnv(t *testing.T) {
	for _, key := range []string{
		dotEnvFileEnvironmentVariable,
		"HTTP_ADDR",
		"DB_PATH",
		"PROJECTS_ROOT",
		"BACKUP_ROOT",
		"SYSTEMD_UNIT_DIR",
		"SYSTEMD_BINARY",
		"SYSTEMD_SCOPE",
		"BACKUP_CONTAINER_NAME",
		"BACKUP_DOCKER_BINARY",
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"GOOGLE_REDIRECT_URL",
		"AUTH_SESSION_SECRET",
		"AUTH_COOKIE_SECURE",
	} {
		unsetEnvironment(t, key)
	}

	dotEnvPath := filepath.Join(t.TempDir(), "redlaunch.env")
	writeDotEnvTestFile(t, dotEnvPath, strings.Join([]string{
		"GOOGLE_CLIENT_ID=client-from-file",
		"GOOGLE_CLIENT_SECRET=secret-from-file",
		"GOOGLE_REDIRECT_URL=https://example.com/auth/google/callback",
		"AUTH_SESSION_SECRET=ssssssssssssssssssssssssssssssss",
		"AUTH_COOKIE_SECURE=true",
	}, "\n"))
	t.Setenv(dotEnvFileEnvironmentVariable, dotEnvPath)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GoogleAuthEnabled() || cfg.GoogleClientID != "client-from-file" || cfg.GoogleClientSecret != "secret-from-file" || cfg.GoogleRedirectURL != "https://example.com/auth/google/callback" || !cfg.AuthCookieSecure {
		t.Fatalf("configuration loaded from dotenv = %#v", cfg)
	}
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func writeDotEnvTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write dotenv test file: %v", err)
	}
}
