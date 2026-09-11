package service

import (
	"errors"
	"fmt"
	"path/filepath"

	"redlaunch/internal/application"
)

type scopedEnvironmentFile struct {
	snapshot managedFileSnapshot
	contents string
}

func scopedEnvironmentFileNames(serviceName string) (string, string) {
	return serviceName + ".vars.env", serviceName + ".secrets.env"
}

// prepareLegacyDatabaseEnvironment migrates one or more legacy generated
// database services that still read project-wide keys. The original raw
// tokens are copied into service-specific files and the existing service is
// amended to load them after vars.env/secrets.env. Missing or partial state is
// rejected because guessing would risk rotating credentials for a mounted
// database.
func prepareLegacyDatabaseEnvironment(contents, directory string, services []application.Service, serviceType string, varsContents, secretsContents string) (string, []scopedEnvironmentFile, error) {
	legacyCount := 0
	for _, item := range services {
		if item.Type != serviceType {
			continue
		}
		varsName, secretsName := scopedEnvironmentFileNames(item.Name)
		varsSnapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
		if err != nil {
			return "", nil, fmt.Errorf("read %s scoped variables file: %w", item.Name, err)
		}
		secretsSnapshot, err := snapshotManagedFile(filepath.Join(directory, secretsName))
		if err != nil {
			return "", nil, fmt.Errorf("read %s scoped secrets file: %w", item.Name, err)
		}
		if varsSnapshot.exists != secretsSnapshot.exists {
			return "", nil, fmt.Errorf("%w: service %q has only one scoped environment file", application.ErrDatabaseCredentialsAmbiguous, item.Name)
		}
		if !varsSnapshot.exists {
			legacyCount++
		}
	}
	if legacyCount > 1 {
		return "", nil, fmt.Errorf("%w: %d %s services still share project-wide credentials", application.ErrDatabaseCredentialsAmbiguous, legacyCount, serviceType)
	}

	var files []scopedEnvironmentFile
	updated := contents
	for _, item := range services {
		if item.Type != serviceType {
			continue
		}
		varsName, secretsName := scopedEnvironmentFileNames(item.Name)
		varsSnapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
		if err != nil {
			return "", nil, fmt.Errorf("read %s scoped variables file: %w", item.Name, err)
		}
		secretsSnapshot, err := snapshotManagedFile(filepath.Join(directory, secretsName))
		if err != nil {
			return "", nil, fmt.Errorf("read %s scoped secrets file: %w", item.Name, err)
		}
		if varsSnapshot.exists != secretsSnapshot.exists {
			return "", nil, fmt.Errorf("%w: service %q has only one scoped environment file", application.ErrDatabaseCredentialsAmbiguous, item.Name)
		}

		if !varsSnapshot.exists {
			var rawValues map[string]string
			switch serviceType {
			case application.ServiceTypePostgreSQL:
				rawValues, err = requiredRawEnvironmentValues(varsContents, []string{postgresEnvironmentDatabase, postgresEnvironmentUser})
				if err == nil {
					passwordValues, passwordErr := requiredRawEnvironmentValues(secretsContents, []string{postgresEnvironmentPassword})
					if passwordErr != nil {
						err = passwordErr
					} else {
						for key, value := range passwordValues {
							rawValues[key] = value
						}
					}
				}
			case application.ServiceTypeRedis:
				rawValues, err = optionalRawEnvironmentValues(secretsContents, []string{redisEnvironmentPassword})
			default:
				return "", nil, fmt.Errorf("%w: unsupported database service type %q", application.ErrDatabaseCredentialsAmbiguous, serviceType)
			}
			if err != nil {
				return "", nil, fmt.Errorf("%w: cannot migrate service %q credentials: %v", application.ErrDatabaseCredentialsAmbiguous, item.Name, err)
			}
			varsScoped, err := rawEnvironmentFile(rawValues, []string{postgresEnvironmentDatabase, postgresEnvironmentUser})
			if serviceType == application.ServiceTypeRedis {
				varsScoped = ""
			}
			if err != nil {
				return "", nil, err
			}
			secretKeys := []string{postgresEnvironmentPassword}
			if serviceType == application.ServiceTypeRedis {
				secretKeys = []string{redisEnvironmentPassword}
			}
			secretsScoped, err := rawEnvironmentFile(rawValues, secretKeys)
			if err != nil {
				return "", nil, err
			}
			files = append(files,
				scopedEnvironmentFile{snapshot: varsSnapshot, contents: varsScoped},
				scopedEnvironmentFile{snapshot: secretsSnapshot, contents: secretsScoped},
			)
		} else if serviceType == application.ServiceTypePostgreSQL {
			if _, _, err := findEnvironmentVariableToken(string(varsSnapshot.contents), postgresEnvironmentDatabase); err != nil {
				return "", nil, fmt.Errorf("%w: service %q has no scoped PostgreSQL database name: %v", application.ErrDatabaseCredentialsAmbiguous, item.Name, err)
			}
			if _, _, err := findEnvironmentVariableToken(string(varsSnapshot.contents), postgresEnvironmentUser); err != nil {
				return "", nil, fmt.Errorf("%w: service %q has no scoped PostgreSQL user: %v", application.ErrDatabaseCredentialsAmbiguous, item.Name, err)
			}
			if _, _, err := findEnvironmentVariableToken(string(secretsSnapshot.contents), postgresEnvironmentPassword); err != nil {
				return "", nil, fmt.Errorf("%w: service %q has no scoped PostgreSQL password: %v", application.ErrDatabaseCredentialsAmbiguous, item.Name, err)
			}
		}

		updated, err = addServiceEnvironmentFiles(updated, item.Name, varsEnvFile, secretsEnvFile, varsName, secretsName)
		if err != nil {
			return "", nil, fmt.Errorf("scope credentials for service %q: %w", item.Name, err)
		}
	}
	return updated, files, nil
}

func preparePostgreSQLScopedFiles(directory, serviceName, databaseName, databaseUser, databasePassword string) ([]scopedEnvironmentFile, string, string, string, error) {
	varsName, secretsName := scopedEnvironmentFileNames(serviceName)
	varsSnapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
	if err != nil {
		return nil, "", "", "", fmt.Errorf("read PostgreSQL scoped variables file: %w", err)
	}
	secretsSnapshot, err := snapshotManagedFile(filepath.Join(directory, secretsName))
	if err != nil {
		return nil, "", "", "", fmt.Errorf("read PostgreSQL scoped secrets file: %w", err)
	}
	if varsSnapshot.exists != secretsSnapshot.exists {
		return nil, "", "", "", fmt.Errorf("%w: service %q has only one scoped environment file", application.ErrDatabaseCredentialsAmbiguous, serviceName)
	}
	if varsSnapshot.exists {
		oldDatabase, err := findEnvironmentVariable(string(varsSnapshot.contents), postgresEnvironmentDatabase)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("%w: read existing PostgreSQL database name: %v", application.ErrDatabaseCredentialsAmbiguous, err)
		}
		oldUser, err := findEnvironmentVariable(string(varsSnapshot.contents), postgresEnvironmentUser)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("%w: read existing PostgreSQL user: %v", application.ErrDatabaseCredentialsAmbiguous, err)
		}
		oldPassword, err := findEnvironmentVariable(string(secretsSnapshot.contents), postgresEnvironmentPassword)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("%w: read existing PostgreSQL password: %v", application.ErrDatabaseCredentialsAmbiguous, err)
		}
		if oldDatabase != databaseName || oldUser != databaseUser || databasePassword != "" && oldPassword != databasePassword {
			return nil, "", "", "", fmt.Errorf("%w: service %q has existing mounted data with different credentials", application.ErrDatabaseCredentialsConflict, serviceName)
		}
		return []scopedEnvironmentFile{
			{snapshot: varsSnapshot, contents: string(varsSnapshot.contents)},
			{snapshot: secretsSnapshot, contents: string(secretsSnapshot.contents)},
		}, oldDatabase, oldUser, oldPassword, nil
	}

	varsContents := upsertEnvironmentWithKeys("", map[string]string{
		postgresEnvironmentDatabase: databaseName,
		postgresEnvironmentUser:     databaseUser,
	}, []string{postgresEnvironmentDatabase, postgresEnvironmentUser})
	secretsContents := upsertEnvironmentWithKeys("", map[string]string{
		postgresEnvironmentPassword: databasePassword,
	}, []string{postgresEnvironmentPassword})
	return []scopedEnvironmentFile{
		{snapshot: varsSnapshot, contents: varsContents},
		{snapshot: secretsSnapshot, contents: secretsContents},
	}, databaseName, databaseUser, databasePassword, nil
}

func prepareRedisScopedFiles(directory, serviceName, password string) ([]scopedEnvironmentFile, string, error) {
	varsName, secretsName := scopedEnvironmentFileNames(serviceName)
	varsSnapshot, err := snapshotManagedFile(filepath.Join(directory, varsName))
	if err != nil {
		return nil, "", fmt.Errorf("read Redis scoped variables file: %w", err)
	}
	secretsSnapshot, err := snapshotManagedFile(filepath.Join(directory, secretsName))
	if err != nil {
		return nil, "", fmt.Errorf("read Redis scoped secrets file: %w", err)
	}
	if varsSnapshot.exists != secretsSnapshot.exists {
		return nil, "", fmt.Errorf("%w: service %q has only one scoped environment file", application.ErrDatabaseCredentialsAmbiguous, serviceName)
	}
	if varsSnapshot.exists {
		oldPassword, err := optionalEnvironmentValue(string(secretsSnapshot.contents), redisEnvironmentPassword)
		if err != nil {
			return nil, "", fmt.Errorf("%w: read existing Redis password: %v", application.ErrDatabaseCredentialsAmbiguous, err)
		}
		if password != "" && oldPassword != password {
			return nil, "", fmt.Errorf("%w: service %q has existing mounted data with a different password", application.ErrDatabaseCredentialsConflict, serviceName)
		}
		return []scopedEnvironmentFile{
			{snapshot: varsSnapshot, contents: string(varsSnapshot.contents)},
			{snapshot: secretsSnapshot, contents: string(secretsSnapshot.contents)},
		}, oldPassword, nil
	}

	secretsContents := ""
	if password != "" {
		secretsContents = upsertEnvironmentWithKeys("", map[string]string{redisEnvironmentPassword: password}, []string{redisEnvironmentPassword})
	}
	return []scopedEnvironmentFile{
		{snapshot: varsSnapshot, contents: ""},
		{snapshot: secretsSnapshot, contents: secretsContents},
	}, password, nil
}

func writeScopedEnvironmentFiles(files []scopedEnvironmentFile) error {
	for _, file := range files {
		mode := file.snapshot.mode
		if mode == 0 {
			mode = envFileMode
		}
		if err := writeManagedFile(file.snapshot.path, file.contents, mode); err != nil {
			return err
		}
	}
	return nil
}

func restoreScopedEnvironmentFiles(files []scopedEnvironmentFile) error {
	var restoreErr error
	for index := len(files) - 1; index >= 0; index-- {
		restoreErr = errors.Join(restoreErr, restoreManagedFile(files[index].snapshot))
	}
	return restoreErr
}

func requiredRawEnvironmentValues(contents string, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		_, raw, err := findEnvironmentVariableToken(contents, key)
		if err != nil {
			return nil, err
		}
		values[key] = raw
	}
	return values, nil
}

func optionalRawEnvironmentValues(contents string, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		_, raw, err := findEnvironmentVariableToken(contents, key)
		if errors.Is(err, application.ErrEnvironmentVariableNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values[key] = raw
	}
	return values, nil
}

func rawEnvironmentFile(values map[string]string, keys []string) (string, error) {
	contents := ""
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var err error
		contents, err = appendRawEnvironmentVariable(contents, key, raw)
		if err != nil {
			return "", err
		}
	}
	return contents, nil
}

func optionalEnvironmentValue(contents, key string) (string, error) {
	value, err := findEnvironmentVariable(contents, key)
	if errors.Is(err, application.ErrEnvironmentVariableNotFound) {
		return "", nil
	}
	return value, err
}
