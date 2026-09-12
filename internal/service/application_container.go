package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	_, err := normalizeApplicationServiceInput(input)
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

	normalizedInput, err := normalizeApplicationServiceInput(input)
	if err != nil {
		return application.Service{}, err
	}
	serviceName := normalizedInput.ServiceName
	imageName := normalizedInput.ImageName
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.Service{}, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.Service{}, err
	}
	if err := s.validateApplicationServiceDependencies(ctx, item.ID, serviceName, normalizedInput.DependsOn); err != nil {
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

	containerName := managedContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName
	reportApplicationContainerProgress(progress, "files", "Writing the application container Compose and environment files")
	composeContents, err := addApplicationServiceWithOptions(string(composeSnapshot.contents), serviceName, imageName, containerName, normalizedInput)
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
	if err := s.validateStagedCompose(ctx, directory); err != nil {
		return application.Service{}, errors.Join(err, restoreManagedFile(composeSnapshot), restoreManagedFile(varsSnapshot), restoreManagedFile(secretsSnapshot))
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
	normalized, err := normalizeApplicationServiceInput(input)
	if err != nil {
		return "", "", err
	}
	return normalized.ServiceName, normalized.ImageName, nil
}

func normalizeApplicationServiceInput(input application.ApplicationServiceInput) (application.ApplicationServiceInput, error) {
	serviceName, err := application.ValidateServiceName(input.ServiceName)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	imageName := strings.TrimSpace(input.ImageName)
	if imageName == "" {
		imageName = application.DefaultApplicationImageName(serviceName)
	}
	imageName, err = application.ValidateImageName(imageName)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}

	entrypoint, err := normalizeApplicationCommand(input.Entrypoint, application.ErrApplicationEntrypointTooLong, application.ErrApplicationEntrypointInvalid)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	healthcheck, err := normalizeApplicationHealthcheck(input.Healthcheck)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	dependencies, err := normalizeApplicationServiceDependencies(input.DependsOn, serviceName)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	restartPolicy, err := normalizeApplicationRestartPolicy(input.RestartPolicy)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	portMappings, err := normalizeApplicationPortMappings(input.PortMappings)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	volumeMappings, err := normalizeApplicationVolumeMappings(input.VolumeMappings)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}

	return application.ApplicationServiceInput{
		ServiceName:    serviceName,
		ImageName:      imageName,
		AutoStart:      input.AutoStart,
		Entrypoint:     entrypoint,
		Healthcheck:    healthcheck,
		DependsOn:      dependencies,
		RestartPolicy:  restartPolicy,
		PortMappings:   portMappings,
		VolumeMappings: volumeMappings,
	}, nil
}

func normalizeApplicationCommand(value string, tooLong, invalid error) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if utf8.RuneCountInString(value) > application.MaxApplicationCommandLength {
		return "", tooLong
	}
	if containsApplicationControlCharacter(value) {
		return "", invalid
	}
	return value, nil
}

func normalizeApplicationHealthcheck(input application.ApplicationHealthcheck) (application.ApplicationHealthcheck, error) {
	command, err := normalizeApplicationCommand(input.Command, application.ErrApplicationHealthcheckCommandTooLong, application.ErrApplicationHealthcheckCommandInvalid)
	if err != nil {
		return application.ApplicationHealthcheck{}, err
	}
	// Compose ignores healthcheck settings without a test. Treat an empty
	// command as an intentionally disabled custom healthcheck and discard any
	// empty form-row defaults along with it.
	if command == "" {
		return application.ApplicationHealthcheck{}, nil
	}

	interval, err := normalizeApplicationDuration(input.Interval, application.ErrApplicationHealthcheckIntervalInvalid, false)
	if err != nil {
		return application.ApplicationHealthcheck{}, err
	}
	timeout, err := normalizeApplicationDuration(input.Timeout, application.ErrApplicationHealthcheckTimeoutInvalid, false)
	if err != nil {
		return application.ApplicationHealthcheck{}, err
	}
	retries := strings.TrimSpace(input.Retries)
	if retries != "" {
		value, parseErr := strconv.Atoi(retries)
		if parseErr != nil || value < 1 || value > 1000 {
			return application.ApplicationHealthcheck{}, application.ErrApplicationHealthcheckRetriesInvalid
		}
	}
	startPeriod, err := normalizeApplicationDuration(input.StartPeriod, application.ErrApplicationHealthcheckStartPeriodInvalid, true)
	if err != nil {
		return application.ApplicationHealthcheck{}, err
	}
	return application.ApplicationHealthcheck{
		Command:     command,
		Interval:    interval,
		Timeout:     timeout,
		Retries:     retries,
		StartPeriod: startPeriod,
	}, nil
}

func normalizeApplicationDuration(value string, invalid error, allowZero bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if utf8.RuneCountInString(value) > 32 || containsApplicationControlCharacter(value) {
		return "", invalid
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 || (!allowZero && duration == 0) {
		return "", invalid
	}
	return value, nil
}

func normalizeApplicationServiceDependencies(values []application.ApplicationServiceDependency, serviceName string) ([]application.ApplicationServiceDependency, error) {
	dependencies := make([]application.ApplicationServiceDependency, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		dependencyName := strings.TrimSpace(value.ServiceName)
		condition := strings.TrimSpace(value.Condition)
		if dependencyName == "" && condition == "" {
			continue
		}
		if dependencyName == "" {
			return nil, application.ErrApplicationDependencyServiceRequired
		}
		dependencyName, err := application.ValidateServiceName(dependencyName)
		if err != nil {
			return nil, application.ErrApplicationDependencyServiceInvalid
		}
		if dependencyName == serviceName {
			return nil, application.ErrApplicationDependencySelf
		}
		if condition == "" {
			condition = application.ApplicationDependencyConditionStarted
		}
		if !validApplicationDependencyCondition(condition) {
			return nil, application.ErrApplicationDependencyConditionInvalid
		}
		if _, exists := seen[dependencyName]; exists {
			return nil, application.ErrApplicationDependencyDuplicate
		}
		seen[dependencyName] = struct{}{}
		dependencies = append(dependencies, application.ApplicationServiceDependency{
			ServiceName: dependencyName,
			Condition:   condition,
		})
	}
	return dependencies, nil
}

func validApplicationDependencyCondition(value string) bool {
	switch value {
	case application.ApplicationDependencyConditionStarted,
		application.ApplicationDependencyConditionHealthy,
		application.ApplicationDependencyConditionCompletedSuccessfully:
		return true
	default:
		return false
	}
}

func normalizeApplicationRestartPolicy(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return application.ApplicationRestartPolicyUnlessStopped, nil
	}
	switch value {
	case application.ApplicationRestartPolicyNo,
		application.ApplicationRestartPolicyAlways,
		application.ApplicationRestartPolicyOnFailure,
		application.ApplicationRestartPolicyUnlessStopped:
		return value, nil
	default:
		return "", application.ErrApplicationRestartPolicyInvalid
	}
}

func normalizeApplicationPortMappings(values []application.ApplicationPortMapping) ([]application.ApplicationPortMapping, error) {
	ports := make([]application.ApplicationPortMapping, 0, len(values))
	for _, value := range values {
		hostPort := strings.TrimSpace(value.HostPort)
		containerPort := strings.TrimSpace(value.ContainerPort)
		protocol := strings.ToLower(strings.TrimSpace(value.Protocol))
		if hostPort == "" && containerPort == "" {
			continue
		}
		if !validApplicationPort(hostPort) || !validApplicationPort(containerPort) {
			return nil, application.ErrApplicationPortMappingInvalid
		}
		if protocol == "" {
			protocol = "tcp"
		}
		switch protocol {
		case "tcp", "udp", "sctp":
		default:
			return nil, application.ErrApplicationPortMappingInvalid
		}
		ports = append(ports, application.ApplicationPortMapping{
			HostPort:      hostPort,
			ContainerPort: containerPort,
			Protocol:      protocol,
		})
	}
	return ports, nil
}

func validApplicationPort(value string) bool {
	if value == "" || len(value) > 5 {
		return false
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func normalizeApplicationVolumeMappings(values []application.ApplicationVolumeMapping) ([]application.ApplicationVolumeMapping, error) {
	volumes := make([]application.ApplicationVolumeMapping, 0, len(values))
	for _, value := range values {
		source := strings.TrimSpace(value.Source)
		target := strings.TrimSpace(value.Target)
		if source == "" && target == "" {
			continue
		}
		if source == "" {
			return nil, application.ErrApplicationVolumeSourceRequired
		}
		if target == "" {
			return nil, application.ErrApplicationVolumeTargetRequired
		}
		if err := validateApplicationVolumeSource(source); err != nil {
			return nil, err
		}
		if err := validateApplicationVolumeTarget(target); err != nil {
			return nil, err
		}
		options, err := normalizeApplicationVolumeOptions(value.Options)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, application.ApplicationVolumeMapping{
			Source:  source,
			Target:  target,
			Options: options,
		})
	}
	return volumes, nil
}

func validateApplicationVolumeSource(value string) error {
	if utf8.RuneCountInString(value) > application.MaxApplicationVolumeSourceLength || containsApplicationControlCharacter(value) || strings.ContainsAny(value, "\\\"':") {
		return application.ErrApplicationVolumeSourceInvalid
	}
	if _, named := composeNamedVolume(value); named {
		return nil
	}
	if value != "." && !strings.HasPrefix(value, "./") {
		return application.ErrApplicationVolumeSourceInvalid
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return application.ErrApplicationVolumeSourceInvalid
	}
	return nil
}

func validateApplicationVolumeTarget(value string) error {
	if utf8.RuneCountInString(value) > application.MaxApplicationVolumeTargetLength || containsApplicationControlCharacter(value) || strings.ContainsAny(value, "\\\"':") || !strings.HasPrefix(value, "/") {
		return application.ErrApplicationVolumeTargetInvalid
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if part == ".." {
			return application.ErrApplicationVolumeTargetInvalid
		}
	}
	return nil
}

func normalizeApplicationVolumeOptions(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "rw", nil
	}
	if utf8.RuneCountInString(value) > application.MaxApplicationMountOptionsLength || containsApplicationControlCharacter(value) || strings.ContainsAny(value, "\\\"':") {
		return "", application.ErrApplicationVolumeOptionsInvalid
	}
	options := strings.Split(value, ",")
	for index, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			return "", application.ErrApplicationVolumeOptionsInvalid
		}
		for _, character := range option {
			if !isApplicationOptionCharacter(character) {
				return "", application.ErrApplicationVolumeOptionsInvalid
			}
		}
		options[index] = option
	}
	return strings.Join(options, ","), nil
}

func isApplicationOptionCharacter(character rune) bool {
	return character == '=' || character == '.' || character == '_' || character == '-' ||
		(character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}

func containsApplicationControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func (s *Applications) validateApplicationServiceDependencies(ctx context.Context, applicationID int64, serviceName string, dependencies []application.ApplicationServiceDependency) error {
	if len(dependencies) == 0 {
		return nil
	}
	registered, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return fmt.Errorf("list application services for dependencies: %w", err)
	}
	known := make(map[string]struct{}, len(registered))
	for _, service := range registered {
		known[service.Name] = struct{}{}
	}
	for _, dependency := range dependencies {
		if dependency.ServiceName == serviceName {
			return application.ErrApplicationDependencySelf
		}
		if _, exists := known[dependency.ServiceName]; !exists {
			return fmt.Errorf("%w: %s", application.ErrApplicationDependencyServiceNotFound, dependency.ServiceName)
		}
	}
	return nil
}

func addApplicationService(contents, serviceName, imageName, containerName string) (string, error) {
	return addApplicationServiceWithOptions(contents, serviceName, imageName, containerName, application.ApplicationServiceInput{
		ServiceName:   serviceName,
		ImageName:     imageName,
		RestartPolicy: application.ApplicationRestartPolicyUnlessStopped,
	})
}

func addApplicationServiceWithOptions(contents, serviceName, imageName, containerName string, input application.ApplicationServiceInput) (string, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok {
		return "", errors.New("Compose file does not define top-level services")
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	if serviceExistsAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2) {
		return "", application.ErrServiceAlreadyExists
	}

	restartPolicy := input.RestartPolicy
	if restartPolicy == "" {
		restartPolicy = application.ApplicationRestartPolicyUnlessStopped
	}
	serviceBlock := []string{
		"  " + serviceName + ":",
		"    image: " + imageName,
		"    container_name: " + containerName,
		"    restart: " + restartPolicy,
		"    env_file:",
		"      - vars.env",
		"      - secrets.env",
	}
	if input.Entrypoint != "" {
		serviceBlock = append(serviceBlock, "    entrypoint: "+composeYAMLString(input.Entrypoint))
	}
	if input.Healthcheck.Command != "" {
		healthcheck := []string{
			"    healthcheck:",
			"      test: [\"CMD-SHELL\", " + composeYAMLString(input.Healthcheck.Command) + "]",
		}
		if input.Healthcheck.Interval != "" {
			healthcheck = append(healthcheck, "      interval: "+input.Healthcheck.Interval)
		}
		if input.Healthcheck.Timeout != "" {
			healthcheck = append(healthcheck, "      timeout: "+input.Healthcheck.Timeout)
		}
		if input.Healthcheck.Retries != "" {
			healthcheck = append(healthcheck, "      retries: "+input.Healthcheck.Retries)
		}
		if input.Healthcheck.StartPeriod != "" {
			healthcheck = append(healthcheck, "      start_period: "+input.Healthcheck.StartPeriod)
		}
		serviceBlock = append(serviceBlock, healthcheck...)
	}
	if len(input.DependsOn) > 0 {
		serviceBlock = append(serviceBlock, "    depends_on:")
		for _, dependency := range input.DependsOn {
			serviceBlock = append(serviceBlock,
				"      "+composeYAMLKey(dependency.ServiceName)+":",
				"        condition: "+dependency.Condition,
			)
		}
	}
	if len(input.PortMappings) > 0 {
		serviceBlock = append(serviceBlock, "    ports:")
		for _, port := range input.PortMappings {
			mapping := port.HostPort + ":" + port.ContainerPort
			if port.Protocol != "tcp" {
				mapping += "/" + port.Protocol
			}
			serviceBlock = append(serviceBlock, "      - "+composeYAMLString(mapping))
		}
	}
	if len(input.VolumeMappings) > 0 {
		serviceBlock = append(serviceBlock, "    volumes:")
		for _, volume := range input.VolumeMappings {
			mapping := volume.Source + ":" + volume.Target
			if volume.Options != "" {
				mapping += ":" + volume.Options
			}
			serviceBlock = append(serviceBlock, "      - "+composeYAMLString(mapping))
		}
	}
	serviceBlock = append(serviceBlock,
		"    networks:",
		"      - default",
		"    labels:",
		"      - \"redlaunch.managed=true\"",
	)

	switch strings.TrimSpace(servicesValue) {
	case "{}":
		lines = replaceYAMLLine(lines, servicesIndex, append([]string{"services:"}, serviceBlock...))
	case "":
		lines = insertYAMLBlock(lines, servicesEnd, serviceBlock)
	default:
		return "", errors.New("Compose services must be a block mapping")
	}

	namedVolumes := applicationNamedVolumes(input.VolumeMappings)
	if len(namedVolumes) > 0 {
		var err error
		lines, err = addApplicationNamedVolumes(lines, namedVolumes)
		if err != nil {
			return "", err
		}
	}

	return strings.Join(lines, "\n"), nil
}

func composeYAMLString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func composeYAMLKey(value string) string {
	switch strings.ToLower(value) {
	case "null", "true", "false", "yes", "no", "on", "off":
		return composeYAMLString(value)
	default:
		return value
	}
}

func applicationNamedVolumes(values []application.ApplicationVolumeMapping) []string {
	seen := make(map[string]struct{}, len(values))
	var names []string
	for _, value := range values {
		name, ok := composeNamedVolume(value.Source)
		if !ok {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func addApplicationNamedVolumes(lines []string, names []string) ([]string, error) {
	volumesIndex, volumesValue, hasVolumes := findTopLevelYAMLKey(lines, "volumes")
	missing := make([]string, 0, len(names))
	if hasVolumes {
		volumesEnd := topLevelBlockEnd(lines, volumesIndex)
		if value := strings.TrimSpace(volumesValue); value != "" && value != "{}" {
			return nil, errors.New("Compose volumes must be a block mapping")
		}
		for _, name := range names {
			if !serviceExistsAtIndent(lines[volumesIndex+1:volumesEnd], name, 2) {
				missing = append(missing, name)
			}
		}
	} else {
		missing = append(missing, names...)
	}
	if len(missing) == 0 {
		return lines, nil
	}

	volumeEntries := make([]string, 0, len(missing)*2)
	for _, name := range missing {
		volumeEntries = append(volumeEntries, "  "+composeYAMLKey(name)+":")
	}
	if !hasVolumes {
		return appendYAMLBlock(lines, append([]string{"volumes:"}, volumeEntries...)), nil
	}
	switch strings.TrimSpace(volumesValue) {
	case "{}":
		return replaceYAMLLine(lines, volumesIndex, append([]string{"volumes:"}, volumeEntries...)), nil
	case "":
		return insertYAMLBlock(lines, topLevelBlockEnd(lines, volumesIndex), volumeEntries), nil
	default:
		return nil, errors.New("Compose volumes must be a block mapping")
	}
}
