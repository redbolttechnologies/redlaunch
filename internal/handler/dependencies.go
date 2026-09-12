package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Dependencies is the explicit production wiring for the HTTP handler.
//
// It replaces the open-ended New(...any) discovery for production code.
// New remains as an explicit test-only constructor so existing HTTP tests can
// assemble partial fakes without authentication. Production startup must use
// NewWithDependencies, which fails fast when required services are missing.
type Dependencies struct {
	Setup          setupManager
	Applications   applicationService
	Backups        serviceBackupService
	GitHubActions  githubActionsService
	Metrics        dashboardMetricsService
	Authentication authenticationService
	SelfUpdate     selfUpdateService
	Security       SecurityConfig

	// AllowUnauthenticatedForTests permits construction without an enabled
	// authentication service. Production code must leave this false so a
	// missing or disabled authenticator fails fast instead of serving
	// management routes without login.
	AllowUnauthenticatedForTests bool
}

// NewWithDependencies constructs the HTTP handler from an explicit dependency
// struct. Required services fail fast; authentication is required unless the
// caller opts into the test-only unauthenticated mode.
func NewWithDependencies(logger *slog.Logger, deps Dependencies) (*Handler, error) {
	if deps.Setup == nil {
		return nil, errors.New("setup service is required")
	}
	if deps.Applications == nil {
		return nil, errors.New("application service is required")
	}
	mode := strings.ToLower(strings.TrimSpace(deps.Security.AccessMode))
	if mode == "" {
		mode = accessModeSSHOnly
	}
	if mode != accessModeSSHOnly && mode != accessModeManagedHTTPS {
		return nil, fmt.Errorf("unsupported management access mode %q", deps.Security.AccessMode)
	}
	if deps.Authentication == nil || !deps.Authentication.Enabled() {
		if !deps.AllowUnauthenticatedForTests {
			return nil, errors.New("authentication service is required for production routes")
		}
	}

	assembled := []any{deps.Setup, deps.Applications}
	if deps.Backups != nil {
		assembled = append(assembled, deps.Backups)
	}
	if deps.GitHubActions != nil {
		assembled = append(assembled, deps.GitHubActions)
	}
	if deps.Metrics != nil {
		assembled = append(assembled, deps.Metrics)
	}
	if deps.SelfUpdate != nil {
		assembled = append(assembled, deps.SelfUpdate)
	}
	if deps.Authentication != nil {
		assembled = append(assembled, deps.Authentication)
	}
	assembled = append(assembled, SecurityConfig{AccessMode: mode, CookieSecure: deps.Security.CookieSecure})

	handler, err := New(logger, assembled...)
	if err != nil {
		return nil, err
	}
	if !deps.AllowUnauthenticatedForTests && !handler.authenticationEnabled() {
		return nil, errors.New("authentication service is required for production routes")
	}
	return handler, nil
}

// NewForTests constructs a handler without requiring authentication. It is a
// thin explicit alias over New so test intent is visible at the call site;
// production code must use NewWithDependencies instead.
func NewForTests(logger *slog.Logger, dependencies ...any) (*Handler, error) {
	return New(logger, dependencies...)
}
