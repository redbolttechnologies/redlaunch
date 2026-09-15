package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

type applicationServiceUpdater interface {
	UpdateServiceImage(context.Context, int64, string, string) (application.Service, error)
}

// GetApplicationServiceConfig reads the current editable configuration for one
// custom application container. The Compose file is the source of truth for
// runtime settings; service metadata supplies the image when the Compose
// value cannot be parsed.
func (s *Applications) GetApplicationServiceConfig(ctx context.Context, applicationID int64, serviceName string) (application.ApplicationServiceInput, error) {
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	if s.detailsRepository == nil {
		return application.ApplicationServiceInput{}, errors.New("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return application.ApplicationServiceInput{}, fmt.Errorf("list application services: %w", err)
	}
	var target *application.Service
	for index := range services {
		if services[index].Name == serviceName {
			target = &services[index]
			break
		}
	}
	if target == nil {
		return application.ApplicationServiceInput{}, application.ErrServiceNotFound
	}
	if target.Type != application.ServiceTypeApplication {
		return application.ApplicationServiceInput{}, application.ErrServiceNotFound
	}

	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return application.ApplicationServiceInput{}, fmt.Errorf("find application Compose file: %w", err)
	}
	if composePath == "" {
		return application.ApplicationServiceInput{}, errors.New("application Compose file does not exist")
	}
	snapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return application.ApplicationServiceInput{}, fmt.Errorf("read application Compose file: %w", err)
	}
	if !snapshot.exists {
		return application.ApplicationServiceInput{}, errors.New("application Compose file does not exist")
	}

	config, err := parseApplicationServiceBlock(string(snapshot.contents), serviceName)
	if err != nil {
		return application.ApplicationServiceInput{}, err
	}
	if target.ImageName != "" {
		config.ImageName = target.ImageName
	}
	config.ServiceName = serviceName
	return config, nil
}

// UpdateApplicationService rewrites one custom application container
// definition, persists its image metadata, and recreates the Compose service
// so the new configuration takes effect.
func (s *Applications) UpdateApplicationService(ctx context.Context, applicationID int64, serviceName string, input application.ApplicationServiceInput) (application.Service, error) {
	return s.UpdateApplicationServiceWithProgress(ctx, applicationID, serviceName, input, nil)
}

// UpdateApplicationServiceWithProgress performs the same update while
// reporting the active workflow stage before each potentially long operation.
func (s *Applications) UpdateApplicationServiceWithProgress(ctx context.Context, applicationID int64, serviceName string, input application.ApplicationServiceInput, progress func(stage, message string)) (application.Service, error) {
	reportApplicationContainerProgress(progress, "configuration", "Preparing application container configuration")
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return application.Service{}, err
	}
	if s.detailsRepository == nil {
		return application.Service{}, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return application.Service{}, errors.New("application service repository is not configured")
	}

	input.ServiceName = serviceName
	normalizedInput, err := normalizeApplicationServiceInput(input)
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
	if target.Type != application.ServiceTypeApplication {
		return application.Service{}, application.ErrServiceNotFound
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
	for _, mapping := range normalizedInput.VolumeMappings {
		if _, named := composeNamedVolume(mapping.Source); named {
			continue
		}
		if err := validateResolvedBindSource(directory, mapping.Source); err != nil {
			return application.Service{}, err
		}
	}
	composeSnapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return application.Service{}, fmt.Errorf("read application Compose file: %w", err)
	}
	if !composeSnapshot.exists {
		return application.Service{}, errors.New("application Compose file does not exist")
	}

	containerName := managedContainerNamePrefix + strconv.FormatInt(item.ID, 10) + "-" + serviceName
	reportApplicationContainerProgress(progress, "files", "Writing the application container Compose file")
	composeContents, err := updateApplicationServiceInCompose(string(composeSnapshot.contents), serviceName, normalizedInput.ImageName, containerName, normalizedInput)
	if err != nil {
		return application.Service{}, err
	}
	if err := writeManagedFile(composePath, composeContents, 0o644); err != nil {
		return application.Service{}, fmt.Errorf("write application Compose file: %w", err)
	}
	if err := s.validateStagedCompose(ctx, directory); err != nil {
		return application.Service{}, errors.Join(err, restoreManagedFile(composeSnapshot))
	}

	reportApplicationContainerProgress(progress, "metadata", "Saving application container metadata")
	updater, ok := s.serviceRepository.(applicationServiceUpdater)
	if !ok {
		return application.Service{}, errors.Join(errors.New("application service update repository is not configured"), restoreManagedFile(composeSnapshot))
	}
	updated, err := updater.UpdateServiceImage(ctx, item.ID, serviceName, normalizedInput.ImageName)
	if err != nil {
		return application.Service{}, errors.Join(fmt.Errorf("persist application service metadata: %w", err), restoreManagedFile(composeSnapshot))
	}

	reportApplicationContainerProgress(progress, "restart", "Recreating the application container with Docker Compose")
	if err := s.startManagedService(ctx, directory, serviceName); err != nil {
		return updated, fmt.Errorf("restart application service: %w", err)
	}
	return updated, nil
}

// updateApplicationServiceInCompose replaces one service block with a newly
// generated managed block and reconciles top-level named volumes.
func updateApplicationServiceInCompose(contents, serviceName, imageName, containerName string, input application.ApplicationServiceInput) (string, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok {
		return "", errors.New("Compose file does not define top-level services")
	}
	if value := strings.TrimSpace(servicesValue); value != "" && value != "{}" {
		return "", errors.New("Compose services must be a block mapping")
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	serviceOffset := findYAMLKeyAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2)
	if serviceOffset < 0 {
		return "", application.ErrServiceNotFound
	}
	serviceIndex := servicesIndex + 1 + serviceOffset
	serviceEnd := servicesEnd
	for index := serviceIndex + 1; index < servicesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); ok {
			serviceEnd = index
			break
		}
	}

	oldVolumes := composeVolumeNamesFromServiceBlock(lines, serviceIndex, serviceEnd)
	serviceBlock := buildApplicationServiceBlock(serviceName, imageName, containerName, input)
	lines = replaceYAMLSection(lines, serviceIndex, serviceEnd, serviceBlock)

	namedVolumes := applicationNamedVolumes(input.VolumeMappings)
	if len(namedVolumes) > 0 {
		var err error
		lines, err = addApplicationNamedVolumes(lines, namedVolumes)
		if err != nil {
			return "", err
		}
	}
	newVolumeSet := make(map[string]struct{}, len(namedVolumes))
	for _, name := range namedVolumes {
		newVolumeSet[name] = struct{}{}
	}
	for _, oldVolume := range oldVolumes {
		if _, kept := newVolumeSet[oldVolume]; kept {
			continue
		}
		var err error
		lines, err = removeUnusedComposeVolume(lines, oldVolume)
		if err != nil {
			return "", err
		}
	}
	return strings.Join(lines, "\n"), nil
}

// parseApplicationServiceBlock extracts the editable fields of one service
// block. Unknown fields are ignored; the edit form overwrites the block with
// the managed representation on save.
func parseApplicationServiceBlock(contents, serviceName string) (application.ApplicationServiceInput, error) {
	lines := strings.Split(contents, "\n")
	servicesIndex, servicesValue, ok := findTopLevelYAMLKey(lines, "services")
	if !ok || strings.TrimSpace(servicesValue) != "" {
		return application.ApplicationServiceInput{}, errors.New("Compose services must be a block mapping")
	}
	servicesEnd := topLevelBlockEnd(lines, servicesIndex)
	serviceOffset := findYAMLKeyAtIndent(lines[servicesIndex+1:servicesEnd], serviceName, 2)
	if serviceOffset < 0 {
		return application.ApplicationServiceInput{}, application.ErrServiceNotFound
	}
	serviceIndex := servicesIndex + 1 + serviceOffset
	serviceEnd := servicesEnd
	for index := serviceIndex + 1; index < servicesEnd; index++ {
		if _, ok := yamlKeyAtIndent(lines[index], 2); ok {
			serviceEnd = index
			break
		}
	}
	block := lines[serviceIndex:serviceEnd]
	config := application.ApplicationServiceInput{ServiceName: serviceName}

	for index := 1; index < len(block); {
		key, value, ok := yamlKeyAtIndentWithValue(block[index], 4)
		if !ok {
			index++
			continue
		}
		fieldEnd := len(block)
		for next := index + 1; next < len(block); next++ {
			if _, ok := yamlKeyAtIndent(block[next], 4); ok {
				fieldEnd = next
				break
			}
		}
		switch key {
		case "image":
			config.ImageName = unquoteComposeScalar(value)
		case "entrypoint":
			config.Entrypoint = parseComposeCommandScalar(value, block[index+1:fieldEnd])
		case "restart":
			config.RestartPolicy = strings.TrimSpace(unquoteComposeScalar(value))
		case "healthcheck":
			config.Healthcheck = parseComposeHealthcheck(block[index+1 : fieldEnd])
		case "depends_on":
			config.DependsOn = parseComposeDependsOn(value, block[index+1:fieldEnd])
		case "ports":
			config.PortMappings = parseComposePorts(value, block[index+1:fieldEnd])
		case "volumes":
			config.VolumeMappings = parseComposeVolumes(value, block[index+1:fieldEnd])
		}
		index = fieldEnd
	}
	if config.RestartPolicy == "" {
		config.RestartPolicy = application.ApplicationRestartPolicyUnlessStopped
	}
	return config, nil
}

func unquoteComposeScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			var decoded string
			if err := json.Unmarshal([]byte(value), &decoded); err == nil {
				return decoded
			}
			return value[1 : len(value)-1]
		}
	}
	return strings.Trim(value, "\"'")
}

func parseComposeCommandScalar(value string, nested []string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		// Block lists are not produced by Redlaunch but may appear in imports.
		// Join the raw lines so the form shows something editable.
		trimmed := make([]string, 0, len(nested))
		for _, line := range nested {
			if text := strings.TrimSpace(line); text != "" && !strings.HasPrefix(text, "#") {
				trimmed = append(trimmed, strings.TrimPrefix(text, "- "))
			}
		}
		return strings.Join(trimmed, " ")
	}
	if strings.HasPrefix(value, "[") {
		var parts []string
		if err := json.Unmarshal([]byte(value), &parts); err == nil {
			return strings.Join(parts, " ")
		}
		return unquoteComposeScalar(value)
	}
	var decoded string
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		return decoded
	}
	return unquoteComposeScalar(value)
}

func parseComposeHealthcheck(nested []string) application.ApplicationHealthcheck {
	var healthcheck application.ApplicationHealthcheck
	for _, line := range nested {
		key, value, ok := yamlKeyAtIndentWithValue(line, 6)
		if !ok {
			continue
		}
		switch key {
		case "test":
			healthcheck.Command = parseComposeHealthcheckTest(value)
		case "interval":
			healthcheck.Interval = strings.TrimSpace(unquoteComposeScalar(value))
		case "timeout":
			healthcheck.Timeout = strings.TrimSpace(unquoteComposeScalar(value))
		case "retries":
			healthcheck.Retries = strings.TrimSpace(unquoteComposeScalar(value))
		case "start_period":
			healthcheck.StartPeriod = strings.TrimSpace(unquoteComposeScalar(value))
		}
	}
	return healthcheck
}

func parseComposeHealthcheckTest(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") {
		return unquoteComposeScalar(value)
	}
	var parts []string
	if err := json.Unmarshal([]byte(value), &parts); err != nil {
		return ""
	}
	for index, part := range parts {
		if part == "CMD-SHELL" && index+1 < len(parts) {
			return strings.Join(parts[index+1:], " ")
		}
	}
	if len(parts) > 0 && parts[0] != "CMD-SHELL" && parts[0] != "CMD" && parts[0] != "NONE" {
		return strings.Join(parts, " ")
	}
	return ""
}

func parseComposeDependsOn(value string, nested []string) []application.ApplicationServiceDependency {
	value = strings.TrimSpace(value)
	if value != "" {
		if strings.HasPrefix(value, "[") {
			var names []string
			if err := json.Unmarshal([]byte(value), &names); err == nil {
				dependencies := make([]application.ApplicationServiceDependency, 0, len(names))
				for _, name := range names {
					if name = strings.TrimSpace(name); name != "" {
						dependencies = append(dependencies, application.ApplicationServiceDependency{
							ServiceName: name,
							Condition:   application.ApplicationDependencyConditionStarted,
						})
					}
				}
				return dependencies
			}
		}
		return nil
	}
	var dependencies []application.ApplicationServiceDependency
	for index := 0; index < len(nested); {
		line := nested[index]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			index++
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			name = unquoteComposeScalar(strings.SplitN(name, ":", 2)[0])
			if name != "" {
				dependencies = append(dependencies, application.ApplicationServiceDependency{
					ServiceName: name,
					Condition:   application.ApplicationDependencyConditionStarted,
				})
			}
			index++
			continue
		}
		key, _, ok := yamlKeyAtIndentWithValue(line, 6)
		if !ok {
			index++
			continue
		}
		condition := application.ApplicationDependencyConditionStarted
		end := len(nested)
		for next := index + 1; next < len(nested); next++ {
			if _, ok := yamlKeyAtIndent(nested[next], 6); ok {
				end = next
				break
			}
			if trimmed := strings.TrimSpace(nested[next]); strings.HasPrefix(trimmed, "-") {
				end = next
				break
			}
		}
		for _, detail := range nested[index+1 : end] {
			detailKey, detailValue, ok := yamlKeyAtIndentWithValue(detail, 8)
			if ok && detailKey == "condition" {
				condition = strings.TrimSpace(unquoteComposeScalar(detailValue))
			}
		}
		dependencies = append(dependencies, application.ApplicationServiceDependency{
			ServiceName: key,
			Condition:   condition,
		})
		index = end
	}
	return dependencies
}

func parseComposePorts(value string, nested []string) []application.ApplicationPortMapping {
	value = strings.TrimSpace(value)
	if value != "" {
		if strings.HasPrefix(value, "[") {
			var entries []string
			if err := json.Unmarshal([]byte(value), &entries); err == nil {
				return parseComposePortEntries(entries)
			}
		}
		return nil
	}
	entries := make([]string, 0, len(nested))
	for _, line := range nested {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "-") {
			continue
		}
		entries = append(entries, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
	}
	return parseComposePortEntries(entries)
}

func parseComposePortEntries(entries []string) []application.ApplicationPortMapping {
	ports := make([]application.ApplicationPortMapping, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(unquoteComposeScalar(strings.TrimSpace(entry)))
		if entry == "" {
			continue
		}
		protocol := "tcp"
		if separator := strings.LastIndexByte(entry, '/'); separator >= 0 {
			protocol = strings.ToLower(strings.TrimSpace(entry[separator+1:]))
			entry = strings.TrimSpace(entry[:separator])
			if protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
				continue
			}
		}
		parts := strings.Split(entry, ":")
		var host, container string
		switch len(parts) {
		case 2:
			host, container = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		case 3:
			host, container = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		default:
			continue
		}
		if host == "" || container == "" {
			continue
		}
		ports = append(ports, application.ApplicationPortMapping{
			HostPort:      host,
			ContainerPort: container,
			Protocol:      protocol,
		})
	}
	return ports
}

func parseComposeVolumes(value string, nested []string) []application.ApplicationVolumeMapping {
	value = strings.TrimSpace(value)
	if value != "" {
		return nil
	}
	entries := make([]string, 0, len(nested))
	for _, line := range nested {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "-") {
			continue
		}
		entries = append(entries, strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
	}
	volumes := make([]application.ApplicationVolumeMapping, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(unquoteComposeScalar(strings.TrimSpace(entry)))
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) < 2 {
			continue
		}
		mapping := application.ApplicationVolumeMapping{
			Source: strings.TrimSpace(parts[0]),
			Target: strings.TrimSpace(parts[1]),
		}
		if len(parts) == 3 {
			mapping.Options = strings.TrimSpace(parts[2])
		}
		if mapping.Options == "" {
			mapping.Options = "rw"
		}
		if mapping.Source == "" || mapping.Target == "" {
			continue
		}
		volumes = append(volumes, mapping)
	}
	return volumes
}
