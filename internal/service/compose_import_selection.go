package service

import (
	"context"
	"fmt"
	"strings"

	"redlaunch/internal/application"
)

// PreviewDockerComposeProject returns a read-only summary of the uploaded
// Compose file for the import dialog. Policy validation still runs so the
// dialog can explain rejections, but the preview is returned alongside the
// policy error when parsing succeeded.
func (s *Applications) PreviewDockerComposeProject(ctx context.Context, applicationID int64, contents []byte) (*application.ComposeImportPreview, error) {
	if len(contents) == 0 || strings.TrimSpace(string(contents)) == "" {
		return nil, application.ErrComposeFileRequired
	}
	if len(contents) > application.MaxComposeFileSize {
		return nil, application.ErrComposeFileTooLarge
	}
	if s.detailsRepository == nil {
		return nil, fmt.Errorf("application details repository is not configured")
	}
	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	registeredServices, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application services before import: %w", err)
	}
	if len(registeredServices) > 0 {
		return nil, application.ErrComposeServicesAlreadyExist
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return nil, err
	}
	preview, err := PreviewImportedCompose(string(contents))
	if err != nil {
		return nil, err
	}
	if policyErr := validateImportedComposePolicy(string(contents), directory); policyErr != nil {
		return preview, policyErr
	}
	return preview, nil
}

// ImportDockerComposeProjectWithSelection imports only the selected subset of
// an uploaded Compose file. An empty non-selective selection imports
// everything, matching ImportDockerComposeProject.
func (s *Applications) ImportDockerComposeProjectWithSelection(ctx context.Context, applicationID int64, contents []byte, selection application.ComposeImportSelection) ([]application.Service, error) {
	filtered, err := filterImportedComposeSelection(string(contents), selection)
	if err != nil {
		return nil, err
	}
	return s.ImportDockerComposeProject(ctx, applicationID, []byte(filtered))
}

func filterImportedComposeSelection(contents string, selection application.ComposeImportSelection) (string, error) {
	if !selection.Selective {
		return contents, nil
	}
	if len(contents) == 0 || strings.TrimSpace(contents) == "" {
		return "", application.ErrComposeFileRequired
	}
	preview, err := PreviewImportedCompose(contents)
	if err != nil {
		return "", err
	}
	selectedServices, err := normalizeImportSelectionNames(selection.Services, "service")
	if err != nil {
		return "", err
	}
	if len(selectedServices) == 0 {
		return "", fmt.Errorf("%w: select at least one service to import", application.ErrComposeFileInvalid)
	}
	selectedVolumes, err := normalizeImportSelectionNames(selection.Volumes, "volume")
	if err != nil {
		return "", err
	}
	selectedNetworks, err := normalizeImportSelectionNames(selection.Networks, "network")
	if err != nil {
		return "", err
	}
	availableServices := make(map[string]struct{}, len(preview.Services))
	for _, service := range preview.Services {
		availableServices[service.Name] = struct{}{}
	}
	for _, name := range selectedServices {
		if _, ok := availableServices[name]; !ok {
			return "", fmt.Errorf("%w: selected service %q was not found in the uploaded file", application.ErrComposeFileInvalid, name)
		}
	}
	availableVolumes := make(map[string]struct{}, len(preview.Volumes))
	for _, volume := range preview.Volumes {
		availableVolumes[volume.Name] = struct{}{}
	}
	for _, name := range selectedVolumes {
		if _, ok := availableVolumes[name]; !ok {
			return "", fmt.Errorf("%w: selected volume %q was not found in the uploaded file", application.ErrComposeFileInvalid, name)
		}
	}
	availableNetworks := make(map[string]struct{}, len(preview.Networks))
	for _, network := range preview.Networks {
		availableNetworks[network.Name] = struct{}{}
	}
	for _, name := range selectedNetworks {
		if _, ok := availableNetworks[name]; !ok {
			return "", fmt.Errorf("%w: selected network %q was not found in the uploaded file", application.ErrComposeFileInvalid, name)
		}
	}

	lines := strings.Split(contents, "\n")
	parsedServices, err := parseImportedComposeServices(contents)
	if err != nil {
		return "", err
	}
	keepServices := make(map[string]struct{}, len(selectedServices))
	for _, name := range selectedServices {
		keepServices[name] = struct{}{}
	}
	// Remove unselected services from last to first so line offsets stay valid.
	for index := len(parsedServices) - 1; index >= 0; index-- {
		if _, keep := keepServices[parsedServices[index].name]; keep {
			continue
		}
		removeStart := parsedServices[index].start
		if removeStart > 0 && strings.TrimSpace(lines[removeStart-1]) == "" {
			removeStart--
		}
		lines = append(append([]string(nil), lines[:removeStart]...), lines[parsedServices[index].end:]...)
	}

	filtered := strings.Join(lines, "\n")
	keepVolumes := make(map[string]struct{}, len(selectedVolumes))
	for _, name := range selectedVolumes {
		keepVolumes[name] = struct{}{}
	}
	filtered, err = filterImportedTopLevelSection(filtered, "volumes", keepVolumes)
	if err != nil {
		return "", err
	}
	keepNetworks := make(map[string]struct{}, len(selectedNetworks))
	for _, name := range selectedNetworks {
		keepNetworks[name] = struct{}{}
	}
	filtered, err = filterImportedTopLevelSection(filtered, "networks", keepNetworks)
	if err != nil {
		return "", err
	}

	if err := validateImportSelectionReferences(filtered, selectedServices); err != nil {
		return "", err
	}
	if _, err := parseImportedComposeServices(filtered); err != nil {
		return "", err
	}
	return filtered, nil
}

func normalizeImportSelectionNames(names []string, kind string) ([]string, error) {
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		validated, err := application.ValidateServiceName(name)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid selected %s %q", application.ErrComposeFileInvalid, kind, raw)
		}
		if _, exists := seen[validated]; exists {
			continue
		}
		seen[validated] = struct{}{}
		normalized = append(normalized, validated)
	}
	return normalized, nil
}

func filterImportedTopLevelSection(contents, section string, keep map[string]struct{}) (string, error) {
	lines := strings.Split(contents, "\n")
	sectionIndex, sectionValue, ok := findImportedTopLevelYAMLKey(lines, section)
	if !ok {
		if len(keep) > 0 {
			return "", fmt.Errorf("%w: selected %s was not found in the uploaded file", application.ErrComposeFileInvalid, section)
		}
		return contents, nil
	}
	switch importedYAMLValueWithoutComment(sectionValue) {
	case "", "{}", "[]":
		if importedYAMLValueWithoutComment(sectionValue) != "" {
			if len(keep) > 0 {
				return "", fmt.Errorf("%w: selected %s was not found in the uploaded file", application.ErrComposeFileInvalid, section)
			}
			return strings.Join(removeYAMLSection(lines, sectionIndex, topLevelBlockEnd(lines, sectionIndex)), "\n"), nil
		}
	default:
		return "", fmt.Errorf("%w: %s must be a block mapping", application.ErrComposeFileInvalid, section)
	}
	sectionEnd := topLevelBlockEnd(lines, sectionIndex)
	type resourceRange struct {
		name  string
		start int
		end   int
	}
	ranges := make([]resourceRange, 0)
	for index := sectionIndex + 1; index < sectionEnd; {
		key, _, ok := yamlKeyAtIndentWithValue(lines[index], 2)
		if !ok {
			index++
			continue
		}
		name := normalizeImportedYAMLKey(key)
		end := sectionEnd
		for next := index + 1; next < sectionEnd; next++ {
			if _, ok := yamlKeyAtIndent(lines[next], 2); ok {
				end = next
				break
			}
		}
		ranges = append(ranges, resourceRange{name: name, start: index, end: end})
		index = end
	}
	for index := len(ranges) - 1; index >= 0; index-- {
		if _, keep := keep[ranges[index].name]; keep {
			continue
		}
		removeStart := ranges[index].start
		if removeStart > sectionIndex+1 && strings.TrimSpace(lines[removeStart-1]) == "" {
			removeStart--
		}
		lines = append(append([]string(nil), lines[:removeStart]...), lines[ranges[index].end:]...)
		sectionEnd -= ranges[index].end - removeStart
	}
	sectionIndex, _, ok = findImportedTopLevelYAMLKey(lines, section)
	if !ok {
		return strings.Join(lines, "\n"), nil
	}
	sectionEnd = topLevelBlockEnd(lines, sectionIndex)
	if !hasYAMLKeyAtIndent(lines[sectionIndex+1:sectionEnd], 2) {
		lines = removeYAMLSection(lines, sectionIndex, sectionEnd)
	}
	return strings.Join(lines, "\n"), nil
}

// validateImportSelectionReferences fails closed when a kept service still
// references a named volume or non-default network that was deselected.
// Leaving the dangling reference to `docker compose config` would produce a
// less actionable error after files were already staged.
func validateImportSelectionReferences(filtered string, selectedServices []string) error {
	lines := strings.Split(filtered, "\n")
	parsedServices, err := parseImportedComposeServices(filtered)
	if err != nil {
		return err
	}
	declaredVolumes := make(map[string]struct{})
	if _, _, ok := findImportedTopLevelYAMLKey(lines, "volumes"); ok {
		for _, resource := range mustParseTopLevelNames(filtered, "volumes") {
			declaredVolumes[resource] = struct{}{}
		}
	}
	volumeRefs := importedServiceVolumeRefs(lines, parsedServices)
	for volume, users := range volumeRefs {
		if _, declared := declaredVolumes[volume]; declared {
			continue
		}
		// The reference points at a volume with no top-level declaration.
		// Surface which service needs the missing volume instead of leaving
		// a dangling reference for Compose to reject later.
		if names := sortedNames(users); len(names) > 0 {
			return fmt.Errorf("%w: service %q references volume %q which is not selected", application.ErrComposeFileInvalid, names[0], volume)
		}
	}
	declaredNetworks := make(map[string]struct{})
	if _, _, ok := findImportedTopLevelYAMLKey(lines, "networks"); ok {
		for _, resource := range mustParseTopLevelNames(filtered, "networks") {
			declaredNetworks[resource] = struct{}{}
		}
	}
	networkRefs := importedServiceNetworkRefs(lines, parsedServices)
	for network, users := range networkRefs {
		if network == "default" {
			continue
		}
		if _, declared := declaredNetworks[network]; declared {
			continue
		}
		if names := sortedNames(users); len(names) > 0 {
			return fmt.Errorf("%w: service %q references network %q which is not selected", application.ErrComposeFileInvalid, names[0], network)
		}
	}
	return nil
}

func mustParseTopLevelNames(contents, section string) []string {
	resources, err := parseImportedTopLevelResources(contents, section)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(resources))
	for _, resource := range resources {
		names = append(names, resource.name)
	}
	return names
}
