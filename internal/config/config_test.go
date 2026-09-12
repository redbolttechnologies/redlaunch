package config

import (
	"os"
	"strings"
	"testing"
)

func setBackupConfigTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DOTENV_FILE", "")
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("DB_PATH", t.TempDir()+"/redlaunch.db")
	t.Setenv("PROJECTS_ROOT", t.TempDir())
	t.Setenv("BACKUP_ROOT", t.TempDir())
	t.Setenv("REDLAUNCH_DIR", t.TempDir())
	t.Setenv("SYSTEMD_UNIT_DIR", t.TempDir())
	t.Setenv("SYSTEMD_BINARY", "systemctl")
	t.Setenv("SYSTEMD_SCOPE", "system")
	t.Setenv("BACKUP_DOCKER_BINARY", "/usr/bin/docker")
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	t.Setenv("GOOGLE_REDIRECT_URL", "")
	t.Setenv("AUTH_SESSION_SECRET", "")
	t.Setenv("AUTH_COOKIE_SECURE", "false")
	t.Setenv("MANAGEMENT_ACCESS_MODE", AccessModeSSHOnly)
	t.Setenv("METRICS_SCOPE", MetricsScopeManager)
	t.Setenv("METRICS_PROC_ROOT", "")
	t.Setenv("METRICS_FILESYSTEM_ROOT", "")
}

func TestLoadAcceptsGoogleAuthenticationConfiguration(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("GOOGLE_REDIRECT_URL", "https://redlaunch.example.com/auth/google/callback")
	t.Setenv("AUTH_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("AUTH_COOKIE_SECURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GoogleAuthEnabled() || cfg.GoogleClientID != "client-id" || cfg.GoogleClientSecret != "client-secret" || cfg.GoogleRedirectURL != "https://redlaunch.example.com/auth/google/callback" || !cfg.AuthCookieSecure {
		t.Fatalf("Google authentication configuration = %#v", cfg)
	}
}

func TestLoadDefaultsStandaloneHTTPListenerToLoopback(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	if err := os.Unsetenv("HTTP_ADDR"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr = %q, want loopback default", cfg.HTTPAddr)
	}
}

func TestLoadRejectsIncompleteGoogleAuthenticationConfiguration(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("GOOGLE_CLIENT_ID", "client-id")

	if _, err := Load(); err == nil {
		t.Fatal("Load() returned nil error for incomplete Google authentication configuration")
	}
}

func TestLoadRejectsShortAuthenticationSessionSecret(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("AUTH_SESSION_SECRET", "too-short")

	if _, err := Load(); err == nil {
		t.Fatal("Load() returned nil error for a short authentication session secret")
	}
}

func TestLoadRejectsInvalidAuthenticationCookieSetting(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("AUTH_COOKIE_SECURE", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() returned nil error for invalid AUTH_COOKIE_SECURE")
	}
}

func TestLoadAcceptsContainerBackupsConfiguration(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("BACKUP_CONTAINER_NAME", "redbolt-redlaunch")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackupContainerName != "redbolt-redlaunch" || cfg.BackupDockerBinary != "/usr/bin/docker" {
		t.Fatalf("backup container configuration = %#v, want configured container and Docker binary", cfg)
	}
}

func TestLoadAcceptsUserSystemdScope(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("SYSTEMD_SCOPE", "USER")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemdScope != "user" {
		t.Fatalf("systemd scope = %q, want user", cfg.SystemdScope)
	}
}

func TestLoadRejectsUnsupportedSystemdScope(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("SYSTEMD_SCOPE", "session")

	if _, err := Load(); err == nil {
		t.Fatal("Load() returned nil error for unsupported systemd scope")
	}
}

func TestLoadRejectsUnsafeBackupContainerName(t *testing.T) {
	for _, value := range []string{"../redlaunch", "-redlaunch", "red launch"} {
		t.Run(value, func(t *testing.T) {
			setBackupConfigTestEnvironment(t)
			t.Setenv("BACKUP_CONTAINER_NAME", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with BACKUP_CONTAINER_NAME=%q returned nil error", value)
			}
		})
	}
}

func TestLoadAcceptsManagedHTTPSAccessMode(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("MANAGEMENT_ACCESS_MODE", "MANAGED-HTTPS")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagementAccessMode != AccessModeManagedHTTPS {
		t.Fatalf("ManagementAccessMode = %q, want %q", cfg.ManagementAccessMode, AccessModeManagedHTTPS)
	}
}

func TestLoadRejectsUnsupportedManagementAccessMode(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("MANAGEMENT_ACCESS_MODE", "public-http")

	if _, err := Load(); err == nil {
		t.Fatal("Load() returned nil error for unsupported management access mode")
	}
}

func TestLoadAcceptsExplicitVPSMetricsPaths(t *testing.T) {
	setBackupConfigTestEnvironment(t)
	t.Setenv("METRICS_SCOPE", "VPS")
	t.Setenv("METRICS_PROC_ROOT", "/host/proc")
	t.Setenv("METRICS_FILESYSTEM_ROOT", "/host/root")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsScope != MetricsScopeVPS || cfg.MetricsProcRoot != "/host/proc" || cfg.MetricsFilesystemRoot != "/host/root" {
		t.Fatalf("metrics configuration = %#v", cfg)
	}
}

func TestLoadRejectsInvalidMetricsConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "scope", key: "METRICS_SCOPE", value: "host"},
		{name: "proc root", key: "METRICS_PROC_ROOT", value: "proc"},
		{name: "filesystem root", key: "METRICS_FILESYSTEM_ROOT", value: "root"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			setBackupConfigTestEnvironment(t)
			t.Setenv(testCase.key, testCase.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() returned nil error for invalid %s", testCase.key)
			}
		})
	}
}
