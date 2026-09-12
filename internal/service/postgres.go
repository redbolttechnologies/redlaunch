package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

const (
	postgresEnvironmentDatabase = "POSTGRES_DB"
	postgresEnvironmentUser     = "POSTGRES_USER"
	postgresEnvironmentPassword = "POSTGRES_PASSWORD"
	managedContainerNamePrefix  = "redbolt-"
	postgresContainerNamePrefix = managedContainerNamePrefix
	postgresComposeVolumeSuffix = "_data"
)

// CreatePostgreSQLService creates a PostgreSQL service definition for an
// existing application, persists its non-secret metadata, and starts the
// requested Compose service.
func (s *Applications) CreatePostgreSQLService(ctx context.Context, applicationID int64, input application.PostgreSQLServiceInput) (application.Service, error) {
	return s.CreatePostgreSQLServiceWithProgress(ctx, applicationID, input, nil)
}

// ValidatePostgreSQLServiceInput validates the user-supplied PostgreSQL
// service fields without touching the application or Docker.
func (s *Applications) ValidatePostgreSQLServiceInput(input application.PostgreSQLServiceInput) error {
	_, _, _, _, _, err := validatePostgreSQLServiceInput(input)
	return err
}

// CreatePostgreSQLServiceWithProgress performs PostgreSQL service creation and
// reports the active workflow stage before each potentially long operation.
// The callback is synchronous and may be nil.
func (s *Applications) CreatePostgreSQLServiceWithProgress(ctx context.Context, applicationID int64, input application.PostgreSQLServiceInput, progress func(stage, message string)) (application.Service, error) {
	reportPostgreSQLProgress(progress, "configuration", "Preparing PostgreSQL configuration")
	if s.detailsRepository == nil {
		return application.Service{}, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return application.Service{}, errors.New("application service repository is not configured")
	}

	serviceName, postgresVersion, databaseName, databaseUser, databasePassword, err := validatePostgreSQLServiceInput(input)
	if err != nil {
		return application.Service{}, err
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

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.Service{}, err
	}
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return application.Service{}, fmt.Errorf("find application Compose file: %w", err)
	}
	if composePath == "" {
		return application.Service{}, errors.New("application Compose file does not exist")
	}
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

	volumeKey := serviceName + postgresComposeVolumeSuffix
	containerName := postgresContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName
	volumeName := postgresContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName + "-data"
	reportPostgreSQLProgress(progress, "files", "Writing the PostgreSQL Compose and environment files")
	sameTypeServices, err := s.listDatabaseServices(ctx, applicationID, application.ServiceTypePostgreSQL)
	if err != nil {
		return application.Service{}, err
	}
	varsFile, secretsFile := scopedEnvironmentFileNames(serviceName)
	if databasePassword == "" {
		if _, statErr := os.Lstat(filepath.Join(directory, secretsFile)); errors.Is(statErr, os.ErrNotExist) {
			databasePassword, err = generateDatabasePassword()
			if err != nil {
				return application.Service{}, fmt.Errorf("generate PostgreSQL password: %w", err)
			}
		} else if statErr != nil {
			return application.Service{}, fmt.Errorf("inspect PostgreSQL scoped secrets file: %w", statErr)
		}
	}
	scopedFiles, effectiveDatabaseName, effectiveDatabaseUser, effectiveDatabasePassword, err := preparePostgreSQLScopedFiles(directory, serviceName, databaseName, databaseUser, databasePassword)
	if err != nil {
		return application.Service{}, err
	}
	databaseName, databaseUser, databasePassword = effectiveDatabaseName, effectiveDatabaseUser, effectiveDatabasePassword
	composeContents, err := addPostgreSQLService(string(composeSnapshot.contents), serviceName, postgresVersion, containerName, volumeKey, volumeName, varsFile, secretsFile)
	if err != nil {
		return application.Service{}, fmt.Errorf("add PostgreSQL service to Compose file: %w", err)
	}
	composeContents, legacyFiles, err := prepareLegacyDatabaseEnvironment(composeContents, directory, sameTypeServices, application.ServiceTypePostgreSQL, string(varsSnapshot.contents), string(secretsSnapshot.contents))
	if err != nil {
		return application.Service{}, err
	}
	scopedFiles = append(scopedFiles, legacyFiles...)
	varsContents := string(varsSnapshot.contents)
	secretsContents := string(secretsSnapshot.contents)
	if len(sameTypeServices) == 0 {
		varsContents = upsertEnvironment(varsContents, map[string]string{
			postgresEnvironmentDatabase: databaseName,
			postgresEnvironmentUser:     databaseUser,
		})
		secretsContents = upsertEnvironment(secretsContents, map[string]string{
			postgresEnvironmentPassword: databasePassword,
		})
	}

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
	if err := writeScopedEnvironmentFiles(scopedFiles); err != nil {
		return application.Service{}, errors.Join(fmt.Errorf("write PostgreSQL scoped environment files: %w", err), restoreManagedFile(composeSnapshot), restoreManagedFile(varsSnapshot), restoreManagedFile(secretsSnapshot), restoreScopedEnvironmentFiles(scopedFiles))
	}
	if err := s.validateStagedCompose(ctx, directory); err != nil {
		return application.Service{}, errors.Join(err, restoreManagedFile(composeSnapshot), restoreManagedFile(varsSnapshot), restoreManagedFile(secretsSnapshot), restoreScopedEnvironmentFiles(scopedFiles))
	}

	reportPostgreSQLProgress(progress, "metadata", "Saving PostgreSQL service metadata")
	created, err := s.serviceRepository.CreateService(ctx, application.Service{
		ApplicationID:   item.ID,
		Name:            serviceName,
		Type:            application.ServiceTypePostgreSQL,
		ImageName:       "postgres:" + postgresVersion,
		PostgresVersion: postgresVersion,
		DatabaseName:    databaseName,
		DatabaseUser:    databaseUser,
		CreatedAt:       time.Now().UTC(),
	})
	if err != nil {
		composeRestoreErr := restoreManagedFile(composeSnapshot)
		varsRestoreErr := restoreManagedFile(varsSnapshot)
		secretsRestoreErr := restoreManagedFile(secretsSnapshot)
		scopedRestoreErr := restoreScopedEnvironmentFiles(scopedFiles)
		if composeRestoreErr != nil || varsRestoreErr != nil || secretsRestoreErr != nil || scopedRestoreErr != nil {
			return application.Service{}, fmt.Errorf("persist PostgreSQL service metadata: %w (restore files: compose=%v, vars=%v, secrets=%v, scoped=%v)", err, composeRestoreErr, varsRestoreErr, secretsRestoreErr, scopedRestoreErr)
		}
		return application.Service{}, fmt.Errorf("persist PostgreSQL service metadata: %w", err)
	}

	reportPostgreSQLProgress(progress, "start", "Starting the PostgreSQL container with Docker Compose")
	if err := s.startManagedService(ctx, directory, serviceName); err != nil {
		// Keep the definition and metadata when startup fails. This leaves the
		// requested service available for a later retry or manual recovery.
		return created, fmt.Errorf("start PostgreSQL service: %w", err)
	}
	return created, nil
}

func (s *Applications) listDatabaseServices(ctx context.Context, applicationID int64, serviceType string) ([]application.Service, error) {
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application services: %w", err)
	}
	var matches []application.Service
	for _, existing := range services {
		if existing.Type == serviceType {
			matches = append(matches, existing)
		}
	}
	return matches, nil
}

func reportPostgreSQLProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

func validatePostgreSQLServiceInput(input application.PostgreSQLServiceInput) (string, string, string, string, string, error) {
	serviceName, err := application.ValidateServiceName(input.ServiceName)
	if err != nil {
		return "", "", "", "", "", err
	}
	postgresVersion, err := application.ValidatePostgresVersion(input.PostgresVersion)
	if err != nil {
		return "", "", "", "", "", err
	}
	databaseName, err := application.ValidateDatabaseName(input.DatabaseName)
	if err != nil {
		return "", "", "", "", "", err
	}
	databaseUser, err := application.ValidateDatabaseUser(input.DatabaseUser)
	if err != nil {
		return "", "", "", "", "", err
	}
	databasePassword, err := application.ValidateDatabasePassword(input.DatabasePassword)
	if err != nil {
		return "", "", "", "", "", err
	}
	return serviceName, postgresVersion, databaseName, databaseUser, databasePassword, nil
}

func (s *Applications) managedApplicationDirectory(item application.Application) (string, error) {
	folderName, err := application.ValidateFolderName(item.FolderName)
	if err != nil {
		return "", fmt.Errorf("validate stored application folder: %w", err)
	}
	directory := filepath.Join(s.applicationsDir, folderName)
	relative, err := filepath.Rel(s.applicationsDir, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("application directory is outside the managed applications directory")
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", application.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("inspect application directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("application directory must not be a symlink")
	}
	if !info.IsDir() {
		return "", errors.New("application path is not a directory")
	}
	return directory, nil
}

type managedFileSnapshot struct {
	path     string
	exists   bool
	contents []byte
	mode     os.FileMode
}

func snapshotManagedFile(path string) (managedFileSnapshot, error) {
	snapshot := managedFileSnapshot{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return snapshot, errors.New("managed file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return snapshot, errors.New("managed path is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	snapshot.exists = true
	snapshot.contents = contents
	snapshot.mode = info.Mode().Perm()
	return snapshot, nil
}

func restoreManagedFile(snapshot managedFileSnapshot) error {
	if snapshot.exists {
		return writeManagedFile(snapshot.path, string(snapshot.contents), snapshot.mode)
	}
	info, err := os.Lstat(snapshot.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cannot remove symlink while restoring managed file")
	}
	return os.Remove(snapshot.path)
}

func generateDatabasePassword() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

func addPostgreSQLService(contents, serviceName, version, containerName, volumeKey, volumeName, scopedVarsFile, scopedSecretsFile string) (string, error) {
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
		"    image: postgres:" + version,
		"    container_name: " + containerName,
		"    restart: unless-stopped",
		"    env_file:",
		"      - vars.env",
		"      - secrets.env",
		"      - " + scopedVarsFile,
		"      - " + scopedSecretsFile,
		"    healthcheck:",
		"      test: [\"CMD-SHELL\", \"pg_isready -U \\\"$$POSTGRES_USER\\\" -d \\\"$$POSTGRES_DB\\\"\"]",
		"      interval: 10s",
		"      timeout: 5s",
		"      retries: 5",
		"      start_period: 30s",
		"    volumes:",
		"      - " + volumeKey + ":/var/lib/postgresql/data",
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
		if strings.TrimSpace(volumesValue) == "{}" {
			lines = replaceYAMLLine(lines, volumesIndex, append([]string{"volumes:"}, volumeBlock...))
		} else if strings.TrimSpace(volumesValue) == "" {
			lines = insertYAMLBlock(lines, volumesEnd, volumeBlock)
		} else {
			return "", errors.New("Compose volumes must be a block mapping")
		}
	}

	return strings.Join(lines, "\n"), nil
}

func findTopLevelYAMLKey(lines []string, wanted string) (int, string, bool) {
	for index, line := range lines {
		key, value, ok := parseTopLevelYAMLKey(line)
		if ok && key == wanted {
			return index, value, true
		}
	}
	return 0, "", false
}

func parseTopLevelYAMLKey(line string) (string, string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
		return "", "", false
	}
	separator := strings.IndexByte(line, ':')
	if separator < 1 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:separator])
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(line[separator+1:]), true
}

func topLevelBlockEnd(lines []string, start int) int {
	for index := start + 1; index < len(lines); index++ {
		if _, _, ok := parseTopLevelYAMLKey(lines[index]); ok {
			return index
		}
	}
	return len(lines)
}

func serviceExistsAtIndent(lines []string, wanted string, indent int) bool {
	prefix := strings.Repeat(" ", indent)
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) || strings.HasPrefix(line, prefix+" ") || strings.HasPrefix(line, prefix+"\t") {
			continue
		}
		key, _, ok := parseIndentedYAMLKey(line[indent:])
		if ok && key == wanted {
			return true
		}
	}
	return false
}

func parseIndentedYAMLKey(line string) (string, string, bool) {
	separator := strings.IndexByte(line, ':')
	if separator < 1 {
		return "", "", false
	}
	return strings.TrimSpace(line[:separator]), strings.TrimSpace(line[separator+1:]), true
}

func replaceYAMLLine(lines []string, index int, replacement []string) []string {
	result := make([]string, 0, len(lines)-1+len(replacement))
	result = append(result, lines[:index]...)
	result = append(result, replacement...)
	result = append(result, lines[index+1:]...)
	return result
}

func insertYAMLBlock(lines []string, index int, block []string) []string {
	result := make([]string, 0, len(lines)+len(block)+2)
	result = append(result, lines[:index]...)
	if len(result) > 0 && result[len(result)-1] != "" {
		result = append(result, "")
	}
	result = append(result, block...)
	if index < len(lines) && lines[index] != "" {
		result = append(result, "")
	}
	result = append(result, lines[index:]...)
	return result
}

func appendYAMLBlock(lines []string, block []string) []string {
	index := len(lines)
	if index > 0 && lines[index-1] == "" {
		index--
	}
	return insertYAMLBlock(lines, index, block)
}

func upsertEnvironment(contents string, values map[string]string) string {
	return upsertEnvironmentWithKeys(contents, values, []string{
		postgresEnvironmentDatabase,
		postgresEnvironmentUser,
		postgresEnvironmentPassword,
	})
}

func upsertEnvironmentWithKeys(contents string, values map[string]string, keys []string) string {
	lines := strings.Split(contents, "\n")
	if strings.HasSuffix(contents, "\n") {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}

	for index, line := range lines {
		key, ok := parseEnvironmentKey(line)
		if !ok {
			continue
		}
		value, wanted := values[key]
		if wanted {
			lines[index] = key + "=" + formatEnvironmentValue(value)
		}
	}
	for _, key := range keys {
		value, wanted := values[key]
		if wanted && !environmentContainsKey(lines, key) {
			lines = append(lines, key+"="+formatEnvironmentValue(value))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func parseEnvironmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	separator := strings.IndexByte(trimmed, '=')
	if separator < 1 {
		return "", false
	}
	key := trimmed[:separator]
	if !validEnvironmentKey(key) {
		return "", false
	}
	return key, true
}

func validEnvironmentKey(key string) bool {
	for index, character := range key {
		if index == 0 {
			if character != '_' && !isASCIIAlpha(character) {
				return false
			}
			continue
		}
		if character != '_' && !isASCIIAlphaNumeric(character) {
			return false
		}
	}
	return key != ""
}

func environmentContainsKey(lines []string, wanted string) bool {
	for _, line := range lines {
		if key, ok := parseEnvironmentKey(line); ok && key == wanted {
			return true
		}
	}
	return false
}

func formatEnvironmentValue(value string) string {
	if value != "" && environmentValueNeedsNoQuotes(value) {
		return value
	}
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `$$`,
	).Replace(value)
	return `"` + escaped + `"`
}

func environmentValueNeedsNoQuotes(value string) bool {
	for _, character := range value {
		if isASCIIAlphaNumeric(character) || strings.ContainsRune("_./:@%+,-", character) {
			continue
		}
		return false
	}
	return true
}

func isASCIIAlpha(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isASCIIAlphaNumeric(character rune) bool {
	return isASCIIAlpha(character) || character >= '0' && character <= '9'
}
