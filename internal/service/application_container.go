package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

// CreateApplicationService creates a custom application container definition
// for an existing application, persists its image metadata, and optionally
// starts that Compose service.
func (s *Applications) CreateApplicationService(ctx context.Context, applicationID int64, input application.ApplicationServiceInput) (application.Service, error) {
	return s.CreateApplicationServiceWithProgress(ctx, applicationID, input, nil)
}

// ValidateApplicationServiceInput validates the custom application container
// fields without touching the application or Docker.
func (s *Applications) ValidateApplicationServiceInput(input application.ApplicationServiceInput) error {
	_, _, err := validateApplicationServiceInput(input)
	return err
}

// CreateApplicationServiceWithProgress performs custom application container
// creation and reports the active workflow stage before each potentially long
// operation. The callback is synchronous and may be nil.
func (s *Applications) CreateApplicationServiceWithProgress(ctx context.Context, applicationID int64, input application.ApplicationServiceInput, progress func(stage, message string)) (application.Service, error) {
	reportApplicationContainerProgress(progress, "configuration", "Preparing application container configuration")
	if s.detailsRepository == nil {
		return application.Service{}, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return application.Service{}, errors.New("application service repository is not configured")
	}

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.Service{}, err
	}

	serviceName, imageName, err := validateApplicationServiceInput(input)
	if err != nil {
		return application.Service{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.Service{}, err
	}
	composePath := filepath.Join(directory, "compose.yml")
	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)
	composeSnapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return application.Service{}, fmt.Errorf("read application Compose file: %w", err)
	}
	if !composeSnapshot.exists {
		return application.Service{}, errors.New("application Compose file does not exist")
	}
	varsSnapshot, err := snapshotManagedFile(varsPath)
	if err != nil {
		return application.Service{}, fmt.Errorf("read application variables file: %w", err)
	}
	secretsSnapshot, err := snapshotManagedFile(secretsPath)
	if err != nil {
		return application.Service{}, fmt.Errorf("read application secrets file: %w", err)
	}

	containerName := managedContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName
	reportApplicationContainerProgress(progress, "files", "Writing the application container Compose and environment files")
	composeContents, err := addApplicationService(string(composeSnapshot.contents), serviceName, imageName, containerName)
	if err != nil {
		return application.Service{}, fmt.Errorf("add application service to Compose file: %w", err)
	}

	if err := writeManagedFile(composePath, composeContents, 0o644); err != nil {
		return application.Service{}, fmt.Errorf("write application Compose file: %w", err)
	}
	if err := writeManagedFile(varsPath, string(varsSnapshot.contents), envFileMode); err != nil {
		_ = restoreManagedFile(composeSnapshot)
		return application.Service{}, fmt.Errorf("write application variables file: %w", err)
	}
	if err := writeManagedFile(secretsPath, string(secretsSnapshot.contents), envFileMode); err != nil {
		_ = restoreManagedFile(composeSnapshot)
		_ = restoreManagedFile(varsSnapshot)
		return application.Service{}, fmt.Errorf("write application secrets file: %w", err)
	}

	reportApplicationContainerProgress(progress, "metadata", "Saving application container metadata")
	created, err := s.serviceRepository.CreateService(ctx, application.Service{
		ApplicationID: item.ID,
		Name:          serviceName,
		Type:          application.ServiceTypeApplication,
		ImageName:     imageName,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		composeRestoreErr := restoreManagedFile(composeSnapshot)
		varsRestoreErr := restoreManagedFile(varsSnapshot)
		secretsRestoreErr := restoreManagedFile(secretsSnapshot)
		if composeRestoreErr != nil || varsRestoreErr != nil || secretsRestoreErr != nil {
			return application.Service{}, fmt.Errorf("persist application service metadata: %w (restore files: compose=%v, vars=%v, secrets=%v)", err, composeRestoreErr, varsRestoreErr, secretsRestoreErr)
		}
		return application.Service{}, fmt.Errorf("persist application service metadata: %w", err)
	}

	if input.AutoStart {
		reportApplicationContainerProgress(progress, "start", "Starting the application container with Docker Compose")
		if err := s.startManagedService(ctx, directory, serviceName); err != nil {
			// Keep the definition and metadata when startup fails. This leaves the
			// requested container available for a later retry or manual recovery.
			return created, fmt.Errorf("start application service: %w", err)
		}
	}
	return created, nil
}

func reportApplicationContainerProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func validateApplicationServiceInput(input application.ApplicationServiceInput) (string, string, error) {
	serviceName, err := application.ValidateServiceName(input.ServiceName)
	if err != nil {
		return "", "", err
	}
	imageName := strings.TrimSpace(input.ImageName)
	if imageName == "" {
		imageName = application.DefaultApplicationImageName(serviceName)
	}
	imageName, err = application.ValidateImageName(imageName)
	if err != nil {
		return "", "", err
	}
	return serviceName, imageName, nil
}

func addApplicationService(contents, serviceName, imageName, containerName string) (string, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok {
		return "", errors.New("Compose file does not define top-level services")
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	if serviceExistsAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2) {
		return "", application.ErrServiceAlreadyExists
	}

	serviceBlock := []string{
		"  " + serviceName + ":",
		"    image: " + imageName,
		"    container_name: " + containerName,
		"    restart: unless-stopped",
		"    env_file:",
		"      - vars.env",
		"      - secrets.env",
		"    networks:",
		"      - default",
		"    labels:",
		"      - \"redlaunch.managed=true\"",
	}

	switch strings.TrimSpace(servicesValue) {
	case "{}":
		lines = replaceYAMLLine(lines, servicesIndex, append([]string{"services:"}, serviceBlock...))
	case "":
		lines = insertYAMLBlock(lines, servicesEnd, serviceBlock)
	default:
		return "", errors.New("Compose services must be a block mapping")
	}

	return strings.Join(lines, "\n"), nil
}
