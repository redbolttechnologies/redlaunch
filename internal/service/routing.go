package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

const redlaunchPublicUpstream = "http://host.docker.internal:8080"

// GetRedlaunchPublicAccess returns the installation-wide public-access
// settings shown on the application settings tab.
func (s *Applications) GetRedlaunchPublicAccess(ctx context.Context) (application.RedlaunchPublicAccess, error) {
	if s.settingsRepository == nil {
		return application.RedlaunchPublicAccess{}, errors.New("Redlaunch settings repository is not configured")
	}
	return s.settingsRepository.GetRedlaunchPublicAccess(ctx)
}

// UpdateRedlaunchPublicAccess validates and persists the management-interface
// hostname, then applies the complete configuration to Caddy. A failed Caddy
// update restores the previous persisted setting.
func (s *Applications) UpdateRedlaunchPublicAccess(ctx context.Context, input application.RedlaunchPublicAccessInput) error {
	if s.settingsRepository == nil {
		return errors.New("Redlaunch settings repository is not configured")
	}
	settings, err := application.ValidateRedlaunchPublicAccess(input)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previous, err := s.settingsRepository.GetRedlaunchPublicAccess(ctx)
	if err != nil {
		return err
	}
	if settings == previous {
		return nil
	}
	if err := s.settingsRepository.UpdateRedlaunchPublicAccess(ctx, settings); err != nil {
		return err
	}
	if !previous.Enabled && !settings.Enabled {
		return nil
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		if rollbackErr := s.settingsRepository.UpdateRedlaunchPublicAccess(ctx, previous); rollbackErr != nil {
			return fmt.Errorf("apply Caddy Redlaunch configuration: %w (rollback settings: %v)", err, rollbackErr)
		}
		return fmt.Errorf("apply Caddy Redlaunch configuration: %w", err)
	}
	return nil
}

// ListRoutings returns the mappings for one associated domain in creation
// order.
func (s *Applications) ListRoutings(ctx context.Context, applicationID, domainID int64) ([]application.Routing, error) {
	if s.routingRepository == nil {
		return nil, errors.New("application routing repository is not configured")
	}
	return s.routingRepository.ListRoutings(ctx, applicationID, domainID)
}

// CreateRouting validates and persists one mapping, then applies the complete
// routing set to the shared Caddy proxy.
func (s *Applications) CreateRouting(ctx context.Context, applicationID, domainID int64, input application.RoutingInput) (application.Routing, error) {
	item, err := s.prepareRouting(ctx, applicationID, domainID, input)
	if err != nil {
		return application.Routing{}, err
	}
	if s.routingRepository == nil {
		return application.Routing{}, errors.New("application routing repository is not configured")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	created, err := s.routingRepository.CreateRouting(ctx, item)
	if err != nil {
		return application.Routing{}, err
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		rollbackErr := s.routingRepository.DeleteRouting(ctx, created.ApplicationID, created.DomainID, created.ID)
		if rollbackErr != nil {
			return application.Routing{}, fmt.Errorf("apply Caddy routing configuration: %w (rollback routing: %v)", err, rollbackErr)
		}
		return application.Routing{}, fmt.Errorf("apply Caddy routing configuration: %w", err)
	}
	created.DomainName = item.DomainName
	return created, nil
}

// UpdateRouting validates and updates one mapping, then applies the complete
// routing set to the shared Caddy proxy.
func (s *Applications) UpdateRouting(ctx context.Context, applicationID, domainID, routingID int64, input application.RoutingInput) error {
	item, err := s.prepareRouting(ctx, applicationID, domainID, input)
	if err != nil {
		return err
	}
	if s.routingRepository == nil {
		return errors.New("application routing repository is not configured")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previous, err := s.routingRepository.GetRouting(ctx, applicationID, domainID, routingID)
	if err != nil {
		return err
	}
	item.ID = routingID
	if err := s.routingRepository.UpdateRouting(ctx, item); err != nil {
		return err
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		rollbackErr := s.routingRepository.UpdateRouting(ctx, previous)
		if rollbackErr != nil {
			return fmt.Errorf("apply Caddy routing configuration: %w (rollback routing: %v)", err, rollbackErr)
		}
		return fmt.Errorf("apply Caddy routing configuration: %w", err)
	}
	return nil
}

// DeleteRouting removes one mapping and applies the remaining mappings to the
// shared Caddy proxy.
func (s *Applications) DeleteRouting(ctx context.Context, applicationID, domainID, routingID int64) error {
	if s.routingRepository == nil {
		return errors.New("application routing repository is not configured")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previous, err := s.routingRepository.GetRouting(ctx, applicationID, domainID, routingID)
	if err != nil {
		return err
	}
	if err := s.routingRepository.DeleteRouting(ctx, applicationID, domainID, routingID); err != nil {
		return err
	}
	if err := s.refreshProxyConfiguration(ctx); err != nil {
		_, rollbackErr := s.routingRepository.CreateRouting(ctx, previous)
		if rollbackErr != nil {
			return fmt.Errorf("apply Caddy routing configuration: %w (rollback routing: %v)", err, rollbackErr)
		}
		return fmt.Errorf("apply Caddy routing configuration: %w", err)
	}
	return nil
}

func (s *Applications) prepareRouting(ctx context.Context, applicationID, domainID int64, input application.RoutingInput) (application.Routing, error) {
	if s.detailsRepository == nil {
		return application.Routing{}, errors.New("application details repository is not configured")
	}
	if s.domainRepository == nil {
		return application.Routing{}, errors.New("application domain repository is not configured")
	}
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return application.Routing{}, err
	}
	domain, err := s.domainRepository.GetDomain(ctx, item.ID, domainID)
	if err != nil {
		return application.Routing{}, err
	}
	services, err := s.detailsRepository.ListServices(ctx, item.ID)
	if err != nil {
		return application.Routing{}, fmt.Errorf("list application services for routing: %w", err)
	}

	subdomain, err := application.ValidateRoutingSubdomain(input.Subdomain)
	if err != nil {
		return application.Routing{}, err
	}
	path, err := application.ValidateRoutingPath(input.Path)
	if err != nil {
		return application.Routing{}, err
	}
	serviceName, err := application.ValidateServiceName(input.ServiceName)
	if err != nil {
		return application.Routing{}, err
	}
	servicePath, err := application.ValidateRoutingPath(input.ServicePath)
	if err != nil {
		return application.Routing{}, err
	}
	if !routingServiceExists(services, serviceName) {
		return application.Routing{}, application.ErrServiceNotFound
	}
	if len(routingHost(domain.Name, subdomain)) > application.MaxDomainNameLength {
		return application.Routing{}, application.ErrRoutingHostTooLong
	}

	return application.Routing{
		ApplicationID: item.ID,
		DomainID:      domain.ID,
		DomainName:    domain.Name,
		Subdomain:     subdomain,
		Path:          path,
		ServiceName:   serviceName,
		ServicePath:   servicePath,
	}, nil
}

func routingServiceExists(services []application.Service, name string) bool {
	for _, item := range services {
		if item.Name == name {
			return true
		}
	}
	return false
}

func routingHost(domainName, subdomain string) string {
	if subdomain == "" {
		return domainName
	}
	return subdomain + "." + domainName
}

func routingUpstream(item application.Routing) string {
	return managedContainerNamePrefix + strconv.FormatInt(item.ApplicationID, 10) + "-" + item.ServiceName
}

// renderCaddyfile creates the complete Caddy configuration for all persisted
// mappings. Host blocks are grouped so mappings for the same host share one
// Caddy site, and longer path matchers are evaluated first.
func renderCaddyfile(routings []application.Routing, publicAccess application.RedlaunchPublicAccess) string {
	byHost := make(map[string][]application.Routing)
	for _, item := range routings {
		host := routingHost(item.DomainName, item.Subdomain)
		if host == "" {
			continue
		}
		byHost[host] = append(byHost[host], item)
	}
	if publicAccess.Enabled && publicAccess.Domain != "" {
		if _, ok := byHost[publicAccess.Domain]; !ok {
			byHost[publicAccess.Domain] = nil
		}
	}

	hosts := make([]string, 0, len(byHost))
	for host := range byHost {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	var builder strings.Builder
	builder.WriteString("# Routes managed by Redlaunch.\n")
	for _, host := range hosts {
		items := byHost[host]
		sort.SliceStable(items, func(left, right int) bool {
			if len(items[left].Path) != len(items[right].Path) {
				return len(items[left].Path) > len(items[right].Path)
			}
			return items[left].ID < items[right].ID
		})

		builder.WriteString("\n")
		builder.WriteString(host)
		builder.WriteString(" {\n")
		for _, item := range items {
			matcher := "redlaunch_route_" + strconv.FormatInt(item.ID, 10)
			builder.WriteString("    @")
			builder.WriteString(matcher)
			builder.WriteString(" path ")
			builder.WriteString(item.Path)
			builder.WriteString("\n")
			builder.WriteString("    handle @")
			builder.WriteString(matcher)
			builder.WriteString(" {\n")
			builder.WriteString("        rewrite * ")
			builder.WriteString(item.ServicePath)
			builder.WriteString("\n")
			builder.WriteString("        reverse_proxy ")
			builder.WriteString(routingUpstream(item))
			builder.WriteString("\n")
			builder.WriteString("    }\n")
		}
		if publicAccess.Enabled && publicAccess.Domain == host {
			builder.WriteString("    handle {\n")
			builder.WriteString("        reverse_proxy ")
			builder.WriteString(redlaunchPublicUpstream)
			builder.WriteString("\n")
			builder.WriteString("    }\n")
		}
		builder.WriteString("}\n")
	}
	return builder.String()
}

func (s *Applications) refreshProxyConfiguration(ctx context.Context) error {
	if s.routingRepository == nil {
		return errors.New("application routing repository is not configured")
	}
	routings, err := s.routingRepository.ListAllRoutings(ctx)
	if err != nil {
		return fmt.Errorf("list routings for Caddy: %w", err)
	}
	var publicAccess application.RedlaunchPublicAccess
	if s.settingsRepository != nil {
		publicAccess, err = s.settingsRepository.GetRedlaunchPublicAccess(ctx)
		if err != nil {
			return fmt.Errorf("read Redlaunch public access settings for Caddy: %w", err)
		}
	}

	info, err := os.Lstat(s.proxyDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("Caddy proxy is not configured")
	}
	if err != nil {
		return fmt.Errorf("inspect Caddy proxy directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Caddy proxy path is not a directory")
	}

	composePath, err := findProxyComposePath(s.proxyDirectory)
	if err != nil {
		return err
	}
	composeSnapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return fmt.Errorf("read Caddy Compose file: %w", err)
	}
	updatedCompose, composeChanged, err := addProxyCaddyfileMount(string(composeSnapshot.contents))
	if err != nil {
		return fmt.Errorf("prepare Caddy Compose file: %w", err)
	}
	updatedCompose, hostGatewayChanged, err := addProxyHostGateway(updatedCompose)
	if err != nil {
		return fmt.Errorf("prepare Caddy host gateway: %w", err)
	}
	composeChanged = composeChanged || hostGatewayChanged

	caddyPath := filepath.Join(s.proxyDirectory, "Caddyfile")
	caddySnapshot, err := snapshotManagedFile(caddyPath)
	if err != nil {
		return fmt.Errorf("read Caddyfile: %w", err)
	}
	configuration := renderCaddyfile(routings, publicAccess)
	caddyChanged := !caddySnapshot.exists || string(caddySnapshot.contents) != configuration
	if !composeChanged && !caddyChanged {
		return nil
	}

	reloader, hasReloader := s.runner.(proxyReloader)
	if !composeChanged && !hasReloader {
		return errors.New("Caddy proxy reloader is not configured")
	}
	if caddyChanged {
		mode := caddySnapshot.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := writeManagedFileInPlace(caddyPath, configuration, mode); err != nil {
			_ = restoreCaddyfile(caddySnapshot)
			return fmt.Errorf("write Caddyfile: %w", err)
		}
	}
	if composeChanged {
		if err := writeManagedFile(composePath, updatedCompose, composeSnapshot.mode); err != nil {
			_ = restoreCaddyfile(caddySnapshot)
			return fmt.Errorf("write Caddy Compose file: %w", err)
		}
	}

	var applyErr error
	if composeChanged {
		applyErr = s.runner.Up(ctx, s.proxyDirectory)
	} else {
		applyErr = reloader.ReloadProxy(ctx, s.proxyDirectory)
	}
	if applyErr == nil {
		return nil
	}

	if caddyChanged {
		_ = restoreCaddyfile(caddySnapshot)
	}
	if composeChanged {
		_ = restoreManagedFile(composeSnapshot)
		if caddyChanged {
			_ = s.runner.Up(ctx, s.proxyDirectory)
		}
	} else if hasReloader && caddyChanged {
		_ = reloader.ReloadProxy(ctx, s.proxyDirectory)
	}
	return fmt.Errorf("apply Caddy proxy configuration: %w", applyErr)
}

func restoreCaddyfile(snapshot managedFileSnapshot) error {
	if snapshot.exists {
		return writeManagedFileInPlace(snapshot.path, string(snapshot.contents), snapshot.mode)
	}
	return restoreManagedFile(snapshot)
}

func findProxyComposePath(directory string) (string, error) {
	for _, name := range []string{"compose.yml", "compose.yaml"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect Caddy Compose file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("Caddy %s is not a regular file", name)
		}
		return path, nil
	}
	return "", errors.New("Caddy Compose file is not configured")
}

func addProxyCaddyfileMount(contents string) (string, bool, error) {
	if strings.Contains(contents, "./Caddyfile:/etc/caddy/Caddyfile") {
		return contents, false, nil
	}

	lines := strings.Split(contents, "\n")
	for index, line := range lines {
		if line != "    volumes:" {
			continue
		}
		end := len(lines)
		for candidate := index + 1; candidate < len(lines); candidate++ {
			if strings.TrimSpace(lines[candidate]) == "" {
				continue
			}
			indent := len(lines[candidate]) - len(strings.TrimLeft(lines[candidate], " "))
			if indent <= 4 {
				end = candidate
				break
			}
		}
		if end > index && end <= len(lines) && end > 0 && lines[end-1] == "" {
			end--
		}
		mount := "      - ./Caddyfile:/etc/caddy/Caddyfile:ro"
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:end]...)
		updated = append(updated, mount)
		updated = append(updated, lines[end:]...)
		return strings.Join(updated, "\n"), true, nil
	}
	return "", false, errors.New("Caddy Compose file does not define proxy volumes")
}

func addProxyHostGateway(contents string) (string, bool, error) {
	lines := strings.Split(contents, "\n")
	proxyStart := -1
	proxyEnd := len(lines)
	for index, line := range lines {
		if line == "  proxy:" {
			proxyStart = index
			break
		}
	}
	if proxyStart < 0 {
		return "", false, errors.New("Caddy Compose file does not define a proxy service")
	}
	for index := proxyStart + 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "" {
			continue
		}
		indent := len(lines[index]) - len(strings.TrimLeft(lines[index], " "))
		if indent <= 2 {
			proxyEnd = index
			break
		}
	}
	for index := proxyStart + 1; index < proxyEnd; index++ {
		line := strings.Trim(strings.TrimSpace(lines[index]), "\"'")
		if strings.HasPrefix(line, "host.docker.internal:") || strings.Contains(line, "host.docker.internal:host-gateway") {
			return contents, false, nil
		}
	}

	for index := proxyStart + 1; index < proxyEnd; index++ {
		line := lines[index]
		if line != "    extra_hosts:" {
			continue
		}
		end := proxyEnd
		for candidate := index + 1; candidate < proxyEnd; candidate++ {
			if strings.TrimSpace(lines[candidate]) == "" {
				continue
			}
			indent := len(lines[candidate]) - len(strings.TrimLeft(lines[candidate], " "))
			if indent <= 4 {
				end = candidate
				break
			}
		}
		entry := "      - \"host.docker.internal:host-gateway\""
		for candidate := index + 1; candidate < end; candidate++ {
			trimmed := strings.TrimSpace(lines[candidate])
			if trimmed == "" {
				continue
			}
			if !strings.HasPrefix(trimmed, "-") {
				entry = "      host.docker.internal: host-gateway"
			}
			break
		}
		updated := make([]string, 0, len(lines)+1)
		updated = append(updated, lines[:end]...)
		updated = append(updated, entry)
		updated = append(updated, lines[end:]...)
		return strings.Join(updated, "\n"), true, nil
	}

	block := []string{
		"    extra_hosts:",
		"      - \"host.docker.internal:host-gateway\"",
	}
	updated := make([]string, 0, len(lines)+len(block))
	updated = append(updated, lines[:proxyStart+1]...)
	updated = append(updated, block...)
	updated = append(updated, lines[proxyStart+1:]...)
	return strings.Join(updated, "\n"), true, nil
}
