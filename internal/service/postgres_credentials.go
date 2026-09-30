package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"redlaunch/internal/application"
)

// postgresCredentialsInput is validated inside UpdatePostgreSQLCredentials; the
// application package owns the shared validators.
type postgresExecRunner interface {
	ExecPostgresSQL(context.Context, string, string, string) (string, error)
}

type postgresRunningChecker interface {
	IsServiceRunning(context.Context, string, string) (bool, error)
}

type postgresDatabaseUserUpdater interface {
	UpdateServiceDatabaseUser(context.Context, int64, string, string) (application.Service, error)
}

// UpdatePostgreSQLCredentials rotates the database role and/or password for
// one managed PostgreSQL service.
//
// The container must be running: the official postgres image only reads
// POSTGRES_USER/POSTGRES_PASSWORD on first init, so env-file changes alone
// never update the live role. This method applies ALTER ROLE inside the
// running container first, then updates the scoped env files (plus the
// project-wide mirror for a single-Postgres app) and SQLite metadata, and
// finally recreates the container so healthchecks and backups use the new
// credentials. An empty new password keeps the existing password.
func (s *Applications) UpdatePostgreSQLCredentials(ctx context.Context, applicationID int64, serviceName string, input application.PostgreSQLCredentialsInput) (application.Service, error) {
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.Service{}, err
	}
	newUser, err := application.ValidateDatabaseUser(input.DatabaseUser)
	if err != nil {
		return application.Service{}, err
	}
	newPassword := input.DatabasePassword
	keepPassword := newPassword == ""
	if !keepPassword {
		var err error
		newPassword, err = application.ValidateDatabasePassword(newPassword)
		if err != nil {
			return application.Service{}, err
		}
		if newPassword == "" {
			keepPassword = true
		}
	}
	if s.detailsRepository == nil {
		return application.Service{}, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return application.Service{}, errors.New("application service repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.Service{}, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.Service{}, err
	}
	if err := s.ensureApplicationNotDeleting(ctx, applicationID); err != nil {
		return application.Service{}, err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return application.Service{}, fmt.Errorf("list application services: %w", err)
	}
	var target *application.Service
	for index := range services {
		if services[index].Name == serviceName {
			target = &services[index]
			break
		}
	}
	if target == nil {
		return application.Service{}, application.ErrServiceNotFound
	}
	if target.Type != application.ServiceTypePostgreSQL {
		return application.Service{}, application.ErrServiceNotFound
	}

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.Service{}, err
	}
	varsName, secretsName := scopedEnvironmentFileNames(serviceName)
	varsSnapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
	if err != nil {
		return application.Service{}, fmt.Errorf("read PostgreSQL scoped variables file: %w", err)
	}
	secretsSnapshot, err := snapshotManagedFile(filepath.Join(directory, secretsName))
	if err != nil {
		return application.Service{}, fmt.Errorf("read PostgreSQL scoped secrets file: %w", err)
	}
	if !varsSnapshot.exists || !secretsSnapshot.exists {
		return application.Service{}, fmt.Errorf("%w: service %q has no scoped credentials", application.ErrDatabaseCredentialsAmbiguous, serviceName)
	}
	currentUser, err := findEnvironmentVariable(string(varsSnapshot.contents), postgresEnvironmentUser)
	if err != nil {
		return application.Service{}, fmt.Errorf("%w: service %q has no scoped PostgreSQL user", application.ErrDatabaseCredentialsAmbiguous, serviceName)
	}
	currentPassword, err := findEnvironmentVariable(string(secretsSnapshot.contents), postgresEnvironmentPassword)
	if err != nil {
		return application.Service{}, fmt.Errorf("%w: service %q has no scoped PostgreSQL password", application.ErrDatabaseCredentialsAmbiguous, serviceName)
	}
	effectivePassword := currentPassword
	if !keepPassword {
		effectivePassword = newPassword
	}
	if newUser == currentUser && effectivePassword == currentPassword {
		return application.Service{}, application.ErrDatabaseCredentialsUnchanged
	}

	execRunner, ok := s.runner.(postgresExecRunner)
	if !ok {
		return application.Service{}, errors.New("PostgreSQL credential runner is not configured")
	}
	runningChecker, ok := s.runner.(postgresRunningChecker)
	if !ok {
		return application.Service{}, errors.New("PostgreSQL running check is not configured")
	}
	running, err := runningChecker.IsServiceRunning(ctx, directory, serviceName)
	if err != nil {
		return application.Service{}, fmt.Errorf("inspect PostgreSQL service state: %w", err)
	}
	if !running {
		return application.Service{}, application.ErrDatabaseServiceNotRunning
	}

	rename := newUser != currentUser
	if rename {
		if _, err := execRunner.ExecPostgresSQL(ctx, directory, serviceName,
			"ALTER ROLE "+postgresQuoteIdentifier(currentUser)+" RENAME TO "+postgresQuoteIdentifier(newUser)); err != nil {
			return application.Service{}, fmt.Errorf("rename PostgreSQL role: %w", sanitizePostgresError(err, currentUser, newUser, currentPassword, effectivePassword))
		}
	}
	// A rename preserves the existing password; set it explicitly only when
	// the operator supplied a new one.
	if !keepPassword {
		if _, err := execRunner.ExecPostgresSQL(ctx, directory, serviceName,
			"ALTER ROLE "+postgresQuoteIdentifier(newUser)+" WITH PASSWORD "+postgresQuoteLiteral(effectivePassword)); err != nil {
			if rename {
				_, _ = execRunner.ExecPostgresSQL(ctx, directory, serviceName,
					"ALTER ROLE "+postgresQuoteIdentifier(newUser)+" RENAME TO "+postgresQuoteIdentifier(currentUser))
			}
			return application.Service{}, fmt.Errorf("update PostgreSQL password: %w", sanitizePostgresError(err, currentUser, newUser, currentPassword, effectivePassword))
		}
	}

	scopedVarsContents, err := updateEnvironmentFile(string(varsSnapshot.contents), postgresEnvironmentUser, postgresEnvironmentUser, newUser)
	if err != nil {
		_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
		return application.Service{}, fmt.Errorf("write PostgreSQL scoped variables file: %w", err)
	}
	scopedSecretsContents := string(secretsSnapshot.contents)
	if !keepPassword {
		scopedSecretsContents, err = updateEnvironmentFile(string(secretsSnapshot.contents), postgresEnvironmentPassword, postgresEnvironmentPassword, effectivePassword)
		if err != nil {
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, true)
			return application.Service{}, fmt.Errorf("write PostgreSQL scoped secrets file: %w", err)
		}
	}

	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	varsSnapshotProject, err := snapshotManagedFile(varsPath)
	if err != nil {
		_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
		return application.Service{}, fmt.Errorf("read application variables file: %w", err)
	}
	secretsSnapshotProject, err := snapshotManagedFile(secretsPath)
	if err != nil {
		_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
		return application.Service{}, fmt.Errorf("read application secrets file: %w", err)
	}
	mirror := shouldMirrorPostgresCredentials(services, serviceName)
	varsProjectContents := ""
	secretsProjectContents := ""
	if mirror {
		varsProjectContents, secretsProjectContents, err = syncPostgresCredentialsMirror(
			string(varsSnapshotProject.contents), varsSnapshotProject.exists,
			string(secretsSnapshotProject.contents), secretsSnapshotProject.exists,
			newUser, effectivePassword, !keepPassword,
		)
		if err != nil {
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
			return application.Service{}, err
		}
	}

	if err := writeManagedFile(filepath.Join(directory, varsName), scopedVarsContents, envFileMode); err != nil {
		_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
		return application.Service{}, fmt.Errorf("write PostgreSQL scoped variables file: %w", err)
	}
	if !keepPassword {
		if err := writeManagedFile(filepath.Join(directory, secretsName), scopedSecretsContents, envFileMode); err != nil {
			_ = restoreManagedFile(varsSnapshot)
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, true)
			return application.Service{}, fmt.Errorf("write PostgreSQL scoped secrets file: %w", err)
		}
	}
	if mirror {
		if err := writeManagedFile(varsPath, varsProjectContents, envFileMode); err != nil {
			_ = restoreManagedFile(varsSnapshot)
			restoreScopedPostgresFiles(varsSnapshot, secretsSnapshot, keepPassword)
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, !keepPassword)
			return application.Service{}, fmt.Errorf("write application variables file: %w", err)
		}
		if !keepPassword {
			if err := writeManagedFile(secretsPath, secretsProjectContents, envFileMode); err != nil {
				_ = restoreManagedFile(varsSnapshot)
				_ = restoreManagedFile(varsSnapshotProject)
				restoreScopedPostgresFiles(varsSnapshot, secretsSnapshot, keepPassword)
				_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, rename, true)
				return application.Service{}, fmt.Errorf("write application secrets file: %w", err)
			}
		}
	}

	updated := *target
	if rename {
		updater, ok := s.serviceRepository.(postgresDatabaseUserUpdater)
		if !ok {
			_ = restoreManagedFile(varsSnapshot)
			restoreScopedPostgresFiles(varsSnapshot, secretsSnapshot, keepPassword)
			if mirror {
				_ = restoreManagedFile(varsSnapshotProject)
				if !keepPassword {
					_ = restoreManagedFile(secretsSnapshotProject)
				}
			}
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, true, !keepPassword)
			return application.Service{}, errors.New("application service metadata repository is not configured")
		}
		updated, err = updater.UpdateServiceDatabaseUser(ctx, item.ID, serviceName, newUser)
		if err != nil {
			_ = restoreManagedFile(varsSnapshot)
			restoreScopedPostgresFiles(varsSnapshot, secretsSnapshot, keepPassword)
			if mirror {
				_ = restoreManagedFile(varsSnapshotProject)
				if !keepPassword {
					_ = restoreManagedFile(secretsSnapshotProject)
				}
			}
			_ = rollbackPostgresRole(ctx, execRunner, directory, serviceName, currentUser, newUser, currentPassword, effectivePassword, true, !keepPassword)
			return application.Service{}, fmt.Errorf("persist PostgreSQL service metadata: %w", err)
		}
	}

	if err := s.startManagedService(ctx, directory, serviceName); err != nil {
		return updated, fmt.Errorf("restart PostgreSQL service: %w", sanitizePostgresError(err, currentUser, newUser, currentPassword, effectivePassword))
	}
	if _, err := execRunner.ExecPostgresSQL(ctx, directory, serviceName, "SELECT 1"); err != nil {
		return updated, fmt.Errorf("verify PostgreSQL credentials: %w", sanitizePostgresError(err, currentUser, newUser, currentPassword, effectivePassword))
	}
	return updated, nil
}

func rollbackPostgresRole(ctx context.Context, runner postgresExecRunner, directory, serviceName, oldUser, newUser, oldPassword, newPassword string, renamed, passwordChanged bool) error {
	var firstErr error
	if passwordChanged {
		if _, err := runner.ExecPostgresSQL(ctx, directory, serviceName,
			"ALTER ROLE "+postgresQuoteIdentifier(newUser)+" WITH PASSWORD "+postgresQuoteLiteral(oldPassword)); err != nil {
			firstErr = err
		}
		_ = newPassword
	}
	if renamed {
		if _, err := runner.ExecPostgresSQL(ctx, directory, serviceName,
			"ALTER ROLE "+postgresQuoteIdentifier(newUser)+" RENAME TO "+postgresQuoteIdentifier(oldUser)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func restoreScopedPostgresFiles(varsSnapshot, secretsSnapshot managedFileSnapshot, keepPassword bool) {
	_ = restoreManagedFile(varsSnapshot)
	if !keepPassword {
		_ = restoreManagedFile(secretsSnapshot)
	}
}

func shouldMirrorPostgresCredentials(services []application.Service, serviceName string) bool {
	count := 0
	found := false
	for _, service := range services {
		if service.Type != application.ServiceTypePostgreSQL {
			continue
		}
		count++
		if service.Name == serviceName {
			found = true
		}
	}
	return found && count == 1
}

func syncPostgresCredentialsMirror(varsContents string, _ bool, secretsContents string, _ bool, newUser, newPassword string, passwordChanged bool) (string, string, error) {
	// Missing project files are treated as empty: application creation
	// always writes them, but older or imported projects may lack one.
	updatedVars := varsContents
	if _, _, err := findEnvironmentVariableToken(varsContents, postgresEnvironmentUser); err == nil {
		var err error
		updatedVars, err = updateEnvironmentFile(varsContents, postgresEnvironmentUser, postgresEnvironmentUser, newUser)
		if err != nil {
			return "", "", fmt.Errorf("write application variables file: %w", err)
		}
	} else {
		var err error
		updatedVars, err = appendEnvironmentVariable(varsContents, postgresEnvironmentUser, newUser)
		if err != nil {
			return "", "", fmt.Errorf("write application variables file: %w", err)
		}
	}
	updatedSecrets := secretsContents
	if passwordChanged {
		if _, _, err := findEnvironmentVariableToken(secretsContents, postgresEnvironmentPassword); err == nil {
			var err error
			updatedSecrets, err = updateEnvironmentFile(secretsContents, postgresEnvironmentPassword, postgresEnvironmentPassword, newPassword)
			if err != nil {
				return "", "", fmt.Errorf("write application secrets file: %w", err)
			}
		} else {
			var err error
			updatedSecrets, err = appendEnvironmentVariable(secretsContents, postgresEnvironmentPassword, newPassword)
			if err != nil {
				return "", "", fmt.Errorf("write application secrets file: %w", err)
			}
		}
	}
	return updatedVars, updatedSecrets, nil
}

func postgresQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func postgresQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

// sanitizePostgresError strips credential values from runner diagnostics so
// passwords never reach logs or HTTP responses.
func sanitizePostgresError(err error, values ...string) error {
	if err == nil {
		return nil
	}
	detail := err.Error()
	for _, value := range values {
		if value != "" {
			detail = strings.ReplaceAll(detail, value, "[REDACTED]")
		}
	}
	return errors.New(detail)
}

// checkPostgresCredentialsDrift compares the desired POSTGRES_USER in the
// scoped env file with the SQLite metadata. A mismatch means the file was
// edited outside UpdatePostgreSQLCredentials; starting anyway would leave an
// unconnectable database because the image only applies env on first init.
func (s *Applications) checkPostgresCredentialsDrift(ctx context.Context, applicationID int64, target application.Service) error {
	if target.Type != application.ServiceTypePostgreSQL {
		return nil
	}
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return nil
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return nil
	}
	varsName, _ := scopedEnvironmentFileNames(target.Name)
	snapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
	if err != nil || !snapshot.exists {
		return nil
	}
	desired, err := findEnvironmentVariable(string(snapshot.contents), postgresEnvironmentUser)
	if err != nil {
		return nil
	}
	if desired != target.DatabaseUser {
		return fmt.Errorf("%w: service %q wants user %q but the database was created for %q; use Update credentials",
			application.ErrDatabaseCredentialsDrift, target.Name, desired, target.DatabaseUser)
	}
	return nil
}
