package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

const (
	redisEnvironmentPassword = "REDIS_PASSWORD"
	redisComposeVolumeSuffix = "_data"
)

// CreateRedisService creates a Redis service definition for an existing
// application, persists its non-secret metadata, and starts the requested
// Compose service.
func (s *Applications) CreateRedisService(ctx context.Context, applicationID int64, input application.RedisServiceInput) (application.Service, error) {
	return s.CreateRedisServiceWithProgress(ctx, applicationID, input, nil)
}

// ValidateRedisServiceInput validates the user-supplied Redis service fields
// without touching the application or Docker.
func (s *Applications) ValidateRedisServiceInput(input application.RedisServiceInput) error {
	_, _, _, _, err := validateRedisServiceInput(input)
	return err
}

// CreateRedisServiceWithProgress performs Redis service creation and reports
// the active workflow stage before each potentially long operation. The
// callback is synchronous and may be nil.
func (s *Applications) CreateRedisServiceWithProgress(ctx context.Context, applicationID int64, input application.RedisServiceInput, progress func(stage, message string)) (application.Service, error) {
	reportRedisProgress(progress, "configuration", "Preparing Redis configuration")
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

	serviceName, redisVersion, port, password, err := validateRedisServiceInput(input)
	if err != nil {
		return application.Service{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rejectExistingDatabaseServiceType(ctx, applicationID, application.ServiceTypeRedis); err != nil {
		return application.Service{}, err
	}

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

	volumeKey := serviceName + redisComposeVolumeSuffix
	containerName := managedContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName
	volumeName := managedContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName + "-data"
	reportRedisProgress(progress, "files", "Writing the Redis Compose and environment files")
	composeContents, err := addRedisService(string(composeSnapshot.contents), serviceName, redisVersion, port, password != "", input.PersistToDisk, containerName, volumeKey, volumeName)
	if err != nil {
		return application.Service{}, fmt.Errorf("add Redis service to Compose file: %w", err)
	}
	varsContents := string(varsSnapshot.contents)
	secretsValues := make(map[string]string)
	if password != "" {
		secretsValues[redisEnvironmentPassword] = password
	}
	secretsContents := upsertEnvironmentWithKeys(string(secretsSnapshot.contents), secretsValues, []string{redisEnvironmentPassword})

	if err := writeManagedFile(composePath, composeContents, 0o644); err != nil {
		return application.Service{}, fmt.Errorf("write application Compose file: %w", err)
	}
	if err := writeManagedFile(varsPath, varsContents, envFileMode); err != nil {
		_ = restoreManagedFile(composeSnapshot)
		return application.Service{}, fmt.Errorf("write application variables file: %w", err)
	}
	if err := writeManagedFile(secretsPath, secretsContents, envFileMode); err != nil {
		_ = restoreManagedFile(composeSnapshot)
		_ = restoreManagedFile(varsSnapshot)
		_ = restoreManagedFile(secretsSnapshot)
		return application.Service{}, fmt.Errorf("write application secrets file: %w", err)
	}

	reportRedisProgress(progress, "metadata", "Saving Redis service metadata")
	created, err := s.serviceRepository.CreateService(ctx, application.Service{
		ApplicationID:      item.ID,
		Name:               serviceName,
		Type:               application.ServiceTypeRedis,
		ImageName:          "redis:" + redisVersion,
		RedisVersion:       redisVersion,
		RedisPort:          port,
		RedisPersistToDisk: input.PersistToDisk,
		CreatedAt:          time.Now().UTC(),
	})
	if err != nil {
		composeRestoreErr := restoreManagedFile(composeSnapshot)
		varsRestoreErr := restoreManagedFile(varsSnapshot)
		secretsRestoreErr := restoreManagedFile(secretsSnapshot)
		if composeRestoreErr != nil || varsRestoreErr != nil || secretsRestoreErr != nil {
			return application.Service{}, fmt.Errorf("persist Redis service metadata: %w (restore files: compose=%v, vars=%v, secrets=%v)", err, composeRestoreErr, varsRestoreErr, secretsRestoreErr)
		}
		return application.Service{}, fmt.Errorf("persist Redis service metadata: %w", err)
	}

	reportRedisProgress(progress, "start", "Starting the Redis container with Docker Compose")
	if err := s.startManagedService(ctx, directory, serviceName); err != nil {
		// Keep the definition and metadata when startup fails. This leaves the
		// requested service available for a later retry or manual recovery.
		return created, fmt.Errorf("start Redis service: %w", err)
	}
	return created, nil
}

func reportRedisProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func validateRedisServiceInput(input application.RedisServiceInput) (string, string, string, string, error) {
	serviceName, err := application.ValidateServiceName(input.ServiceName)
	if err != nil {
		return "", "", "", "", err
	}
	redisVersion, err := application.ValidateRedisVersion(input.RedisVersion)
	if err != nil {
		return "", "", "", "", err
	}
	port, err := application.ValidateRedisPort(input.Port)
	if err != nil {
		return "", "", "", "", err
	}
	password, err := application.ValidateRedisPassword(input.Password)
	if err != nil {
		return "", "", "", "", err
	}
	return serviceName, redisVersion, port, password, nil
}

func addRedisService(contents, serviceName, version, port string, passwordConfigured, persistToDisk bool, containerName, volumeKey, volumeName string) (string, error) {
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
		"    image: redis:" + version,
		"    container_name: " + containerName,
		"    restart: unless-stopped",
		"    env_file:",
		"      - vars.env",
		"      - secrets.env",
		"    healthcheck:",
		"      test: [\"CMD-SHELL\", \"if [ -n \\\"$$REDIS_PASSWORD\\\" ]; then REDISCLI_AUTH=\\\"$$REDIS_PASSWORD\\\" redis-cli --no-auth-warning ping; else redis-cli ping; fi\"]",
		"      interval: 10s",
		"      timeout: 5s",
		"      retries: 5",
		"      start_period: 30s",
		"    ports:",
		// Redis is published on loopback only. This keeps an optional
		// passwordless cache from becoming reachable on the public interface.
		"      - \"127.0.0.1:" + port + ":6379\"",
		"    networks:",
		"      - default",
		"    labels:",
		"      - \"redlaunch.managed=true\"",
	}

	command := redisCommand(passwordConfigured, persistToDisk)
	if command != "" {
		serviceBlock = append(serviceBlock, "    command: "+command)
	}
	if persistToDisk {
		serviceBlock = append(serviceBlock,
			"    volumes:",
			"      - "+volumeKey+":/data",
		)
	}

	switch strings.TrimSpace(servicesValue) {
	case "{}":
		lines = replaceYAMLLine(lines, servicesIndex, append([]string{"services:"}, serviceBlock...))
	case "":
		lines = insertYAMLBlock(lines, servicesEnd, serviceBlock)
	default:
		return "", errors.New("Compose services must be a block mapping")
	}

	if persistToDisk {
		volumesIndex, volumesValue, hasVolumes := findTopLevelYAMLKey(lines, "volumes")
		volumeBlock := []string{
			"  " + volumeKey + ":",
			"    name: " + volumeName,
		}
		if !hasVolumes {
			lines = appendYAMLBlock(lines, []string{"volumes:", volumeBlock[0], volumeBlock[1]})
		} else {
			volumesEnd := topLevelBlockEnd(lines, volumesIndex)
			if serviceExistsAtIndent(lines[volumesIndex+1:volumesEnd], volumeKey, 2) {
				return "", errors.New("Compose volume already exists")
			}
			switch strings.TrimSpace(volumesValue) {
			case "{}":
				lines = replaceYAMLLine(lines, volumesIndex, append([]string{"volumes:"}, volumeBlock...))
			case "":
				lines = insertYAMLBlock(lines, volumesEnd, volumeBlock)
			default:
				return "", errors.New("Compose volumes must be a block mapping")
			}
		}
	}

	return strings.Join(lines, "\n"), nil
}

func redisCommand(passwordConfigured, persistToDisk bool) string {
	arguments := []string{"redis-server"}
	if !passwordConfigured {
		// The published port is loopback-only, and the application network is
		// the intended trust boundary for passwordless Redis.
		arguments = append(arguments, "--protected-mode", "no")
	}
	if persistToDisk {
		arguments = append(arguments, "--appendonly", "yes", "--save", "1", "1")
	}
	if passwordConfigured {
		// The command is static; the password is read from the container
		// environment and passed as one quoted argument rather than being
		// interpolated into the command or Compose file.
		script := "set -- redis-server"
		if persistToDisk {
			script += " --appendonly yes --save 1 1"
		}
		script += `; if [ -n "$$REDIS_PASSWORD" ]; then set -- "$$@" --requirepass "$$REDIS_PASSWORD"; fi; exec "$$@"`
		arguments = []string{"/bin/sh", "-c", script}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return ""
	}
	return string(encoded)
}
