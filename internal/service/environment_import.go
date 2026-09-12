package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"redlaunch/internal/application"
)

// ImportEnvironmentFiles replaces the selected managed environment files for
// an application. Missing files are created to keep every managed project
// provisioned with both vars.env and secrets.env.
func (s *Applications) ImportEnvironmentFiles(ctx context.Context, applicationID int64, input application.EnvironmentFileImportInput) error {
	if !input.VariablesProvided && !input.SecretsProvided {
		return application.ErrEnvironmentImportFileRequired
	}
	if input.VariablesProvided {
		if err := validateEnvironmentFileContents(input.Variables); err != nil {
			return err
		}
	}
	if input.SecretsProvided {
		if err := validateEnvironmentFileContents(input.Secrets); err != nil {
			return err
		}
	}
	if s.detailsRepository == nil {
		return errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return err
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return err
	}

	varsPath := filepath.Join(directory, varsEnvFile)
	varsSnapshot, err := snapshotManagedFile(varsPath)
	if err != nil {
		return fmt.Errorf("read application variables file: %w", err)
	}
	secretsPath := filepath.Join(directory, secretsEnvFile)
	secretsSnapshot, err := snapshotManagedFile(secretsPath)
	if err != nil {
		return fmt.Errorf("read application secrets file: %w", err)
	}
	rollbackFiles := func() error {
		return errors.Join(restoreManagedFile(varsSnapshot), restoreManagedFile(secretsSnapshot))
	}

	writeFile := func(path, label string, snapshot managedFileSnapshot, contents []byte, provided bool) error {
		if !provided && snapshot.exists {
			return nil
		}
		mode := snapshot.mode
		if !snapshot.exists {
			mode = envFileMode
		}
		if err := writeManagedFile(path, string(contents), mode); err != nil {
			return fmt.Errorf("write application %s file: %w", label, err)
		}
		return nil
	}

	if err := writeFile(varsPath, "variables", varsSnapshot, input.Variables, input.VariablesProvided); err != nil {
		return errors.Join(err, rollbackFiles())
	}
	if err := writeFile(secretsPath, "secrets", secretsSnapshot, input.Secrets, input.SecretsProvided); err != nil {
		return errors.Join(err, rollbackFiles())
	}
	return nil
}
