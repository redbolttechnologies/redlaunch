// Package config loads the small set of runtime settings used by the server.
package config

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

// Config contains the application runtime configuration.
type Config struct {
	HTTPAddr             string
	DatabasePath         string
	ProjectsRoot         string
	BackupRoot           string
	SystemdUnitDirectory string
	SystemdBinary        string
	SystemdScope         string
	BackupContainerName  string
	BackupDockerBinary   string
	GoogleClientID       string
	GoogleClientSecret   string
	GoogleRedirectURL    string
	AuthSessionSecret    string
	AuthCookieSecure     bool
}

// Load reads configuration from the process environment and an optional dotenv
// file, then applies local-safe defaults. Process environment values take
// precedence over values loaded from the dotenv file.
func Load() (Config, error) {
	environment, err := loadEnvironment()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:             valueOrDefault(environment, "HTTP_ADDR", ":8080"),
		DatabasePath:         valueOrDefault(environment, "DB_PATH", "./data/redlaunch.db"),
		ProjectsRoot:         valueOrDefault(environment, "PROJECTS_ROOT", "./projects"),
		BackupRoot:           valueOrDefault(environment, "BACKUP_ROOT", "/var/backups/redlaunch"),
		SystemdUnitDirectory: valueOrDefault(environment, "SYSTEMD_UNIT_DIR", "/etc/systemd/system"),
		SystemdBinary:        valueOrDefault(environment, "SYSTEMD_BINARY", "systemctl"),
		SystemdScope:         valueOrDefault(environment, "SYSTEMD_SCOPE", "system"),
		BackupContainerName:  valueOrDefault(environment, "BACKUP_CONTAINER_NAME", ""),
		BackupDockerBinary:   valueOrDefault(environment, "BACKUP_DOCKER_BINARY", "/usr/bin/docker"),
		GoogleClientID:       strings.TrimSpace(valueOrDefault(environment, "GOOGLE_CLIENT_ID", "")),
		GoogleClientSecret:   valueOrDefault(environment, "GOOGLE_CLIENT_SECRET", ""),
		GoogleRedirectURL:    strings.TrimSpace(valueOrDefault(environment, "GOOGLE_REDIRECT_URL", "http://localhost:8080/auth/google/callback")),
		AuthSessionSecret:    valueOrDefault(environment, "AUTH_SESSION_SECRET", ""),
	}
	if cfg.HTTPAddr == "" {
		return Config{}, errors.New("HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(cfg.DatabasePath) == "" {
		return Config{}, errors.New("DB_PATH must not be empty")
	}
	if strings.TrimSpace(cfg.ProjectsRoot) == "" {
		return Config{}, errors.New("PROJECTS_ROOT must not be empty")
	}
	if strings.TrimSpace(cfg.BackupRoot) == "" {
		return Config{}, errors.New("BACKUP_ROOT must not be empty")
	}
	if strings.TrimSpace(cfg.SystemdUnitDirectory) == "" {
		return Config{}, errors.New("SYSTEMD_UNIT_DIR must not be empty")
	}
	if strings.TrimSpace(cfg.SystemdBinary) == "" {
		return Config{}, errors.New("SYSTEMD_BINARY must not be empty")
	}
	cfg.SystemdScope = strings.ToLower(strings.TrimSpace(cfg.SystemdScope))
	if cfg.SystemdScope != "system" && cfg.SystemdScope != "user" {
		return Config{}, errors.New("SYSTEMD_SCOPE must be system or user")
	}
	if cfg.BackupContainerName != "" && !validContainerName(cfg.BackupContainerName) {
		return Config{}, errors.New("BACKUP_CONTAINER_NAME is invalid")
	}
	if strings.TrimSpace(cfg.BackupDockerBinary) == "" {
		return Config{}, errors.New("BACKUP_DOCKER_BINARY must not be empty")
	}
	authConfigured := cfg.GoogleClientID != "" || strings.TrimSpace(cfg.GoogleClientSecret) != ""
	if authConfigured {
		if cfg.GoogleClientID == "" || strings.TrimSpace(cfg.GoogleClientSecret) == "" {
			return Config{}, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be configured together")
		}
		if strings.TrimSpace(cfg.GoogleRedirectURL) == "" {
			return Config{}, errors.New("GOOGLE_REDIRECT_URL must not be empty when Google authentication is enabled")
		}
		if len(cfg.AuthSessionSecret) < 32 {
			return Config{}, errors.New("AUTH_SESSION_SECRET must contain at least 32 bytes when Google authentication is enabled")
		}
	}
	cfg.AuthCookieSecure, err = boolValue(environment, "AUTH_COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}
	if filepath.Clean(cfg.ProjectsRoot) == string(filepath.Separator) {
		return Config{}, errors.New("PROJECTS_ROOT must not be the filesystem root")
	}
	return cfg, nil
}

// GoogleAuthEnabled reports whether both Google credentials are configured.
// The server entry point uses this to fail closed when authentication is not
// configured; the authorized-email command can still open the database before
// the server is started.
func (c Config) GoogleAuthEnabled() bool {
	return c.GoogleClientID != "" && strings.TrimSpace(c.GoogleClientSecret) != ""
}

func valueOrDefault(environment map[string]string, key, fallback string) string {
	value, ok := environment[key]
	if !ok {
		return fallback
	}
	return value
}

func boolValue(environment map[string]string, key string, fallback bool) (bool, error) {
	value, ok := environment[key]
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, errors.New(key + " must be true or false")
	}
	return parsed, nil
}

func validContainerName(value string) bool {
	if len(value) > 128 {
		return false
	}
	for index, character := range value {
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if !alphaNumeric && character != '_' && character != '.' && character != '-' {
			return false
		}
	}
	return value != ""
}
