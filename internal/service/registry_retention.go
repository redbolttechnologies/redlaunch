package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"redlaunch/internal/application"
)

// registryMaintenanceService is the registry capability used by both
// scheduled and manual maintenance.
type registryMaintenanceService interface {
	ApplyMaintenance(context.Context, RegistryMaintenanceOptions) (RegistryMaintenanceResult, error)
}

// SetRegistryMaintenance supplies the shared read-only registry maintenance
// implementation.
func (s *Applications) SetRegistryMaintenance(service registryMaintenanceService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registryMaintenance = service
}

func (s *Applications) registryMaintenanceService() registryMaintenanceService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.registryMaintenance
}

// RetainRegistryVersions runs the shared retention policy immediately, for the
// manual Purge images action.
func (s *Applications) RetainRegistryVersions(ctx context.Context) (RegistryMaintenanceResult, error) {
	return s.runRegistryMaintenance(ctx, true, false, false)
}

// CollectRegistryGarbage runs garbage collection immediately, for the manual
// Run garbage collection action.
func (s *Applications) CollectRegistryGarbage(ctx context.Context) (RegistryMaintenanceResult, error) {
	return s.runRegistryMaintenance(ctx, false, true, false)
}

// RunScheduledRegistryMaintenance runs due retention and garbage collection.
func (s *Applications) RunScheduledRegistryMaintenance(ctx context.Context, retention, garbageCollection bool) (RegistryMaintenanceResult, error) {
	return s.runRegistryMaintenance(ctx, retention, garbageCollection, true)
}

func (s *Applications) runRegistryMaintenance(ctx context.Context, retention, garbageCollection, scheduled bool) (RegistryMaintenanceResult, error) {
	maintenance := s.registryMaintenanceService()
	if maintenance == nil {
		return RegistryMaintenanceResult{}, errors.New("registry maintenance is not configured")
	}
	items, err := s.List(ctx)
	if err != nil {
		return RegistryMaintenanceResult{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, item := range items {
		lease, err := s.acquireApplicationProject(ctx, item.ID)
		if err != nil {
			return RegistryMaintenanceResult{}, err
		}
		defer lease.release()
	}
	protected := make(map[string]bool)
	if retention {
		protected, err = s.protectedRegistryVersions(ctx, items)
		if err != nil {
			return RegistryMaintenanceResult{}, err
		}
	}
	return maintenance.ApplyMaintenance(ctx, RegistryMaintenanceOptions{
		Retention:         retention,
		GarbageCollection: garbageCollection,
		Protected:         protected,
		Scheduled:         scheduled,
	})
}

// protectedRegistryVersions holds application project leases already; it
// inspects runtime containers and configured images before any registry
// deletion.
func (s *Applications) protectedRegistryVersions(ctx context.Context, items []application.Application) (map[string]bool, error) {
	inspector, ok := s.runner.(composeServiceInspector)
	if !ok {
		return nil, errors.New("container inspection is not configured")
	}
	resolver, ok := s.runner.(composeServiceImageResolver)
	if !ok {
		return nil, errors.New("service image resolution is not configured")
	}
	reader, ok := s.runner.(composeContainerDigestReader)
	if !ok {
		return nil, errors.New("container image inspection is not configured")
	}
	protected := make(map[string]bool)
	for _, item := range items {
		services, err := s.detailsRepository.ListServices(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		directory, err := s.managedApplicationDirectory(item)
		if err != nil {
			return nil, err
		}
		runtime, err := inspector.ListServices(ctx, directory)
		if err != nil {
			return nil, fmt.Errorf("inspect services before registry retention: %w", err)
		}
		for _, container := range runtime {
			reference, local := strings.CutPrefix(container.Image, application.LocalRegistryAddress+"/")
			if !local || container.ContainerName == "" {
				continue
			}
			repository := strings.SplitN(strings.SplitN(reference, "@", 2)[0], ":", 2)[0]
			if _, err := application.ValidateRegistryRepository(repository); err != nil {
				return nil, err
			}
			digest, err := reader.ContainerImageDigest(ctx, container.ContainerName, application.LocalRegistryAddress+"/"+repository)
			if err != nil || digest == "" {
				return nil, errors.New("runtime image could not be verified before registry retention")
			}
			protected[application.RegistryImageKey(repository, digest)] = true
		}
		for _, service := range services {
			image, err := resolver.ResolveServiceImage(ctx, directory, service.Name)
			if err != nil {
				return nil, fmt.Errorf("resolve service image before registry retention: %w", err)
			}
			image = strings.TrimSpace(image)
			reference, local := strings.CutPrefix(image, application.LocalRegistryAddress+"/")
			if !local {
				continue
			}
			// Digest-pinned services are protected even without a container.
			if repository, digest, pinned := strings.Cut(reference, "@"); pinned {
				repository = strings.SplitN(repository, ":", 2)[0]
				if _, err := application.ValidateRegistryRepository(repository); err != nil {
					return nil, err
				}
				if _, err := application.ValidateRegistryDigest(digest); err != nil {
					return nil, err
				}
				protected[application.RegistryImageKey(repository, digest)] = true
				continue
			}
			// An omitted tag means latest in Docker.
			if !strings.Contains(reference, ":") {
				image += ":latest"
			}
			repository, tag, valid := application.ParseDeployedRegistryReference(image)
			if !valid {
				return nil, errors.New("invalid local registry service reference")
			}
			protected[application.RegistryImageKey(repository, tag)] = true
			for _, container := range runtime {
				if container.ServiceName != service.Name || container.ContainerName == "" {
					continue
				}
				digest, err := reader.ContainerImageDigest(ctx, container.ContainerName, application.LocalRegistryAddress+"/"+repository)
				if err != nil || digest == "" {
					return nil, errors.New("deployed image could not be verified before registry retention")
				}
				protected[application.RegistryImageKey(repository, digest)] = true
			}
		}
	}
	return protected, nil
}

// RunRegistryMaintenanceWorker checks recovery and due maintenance at startup
// and then hourly while Redlaunch is serving; retention and garbage collection
// are each due at most once per day. Each pass is bounded and cancellation
// stops work while the maintenance lifecycle restores registry writability.
func (s *Applications) RunRegistryMaintenanceWorker(ctx context.Context, registry *RegistryService, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		passCtx, cancel := context.WithTimeout(ctx, RegistryMaintenanceTimeout)
		if registry.MaintenanceError() != nil {
			if err := registry.RecoverRegistryMaintenance(passCtx); err != nil && ctx.Err() == nil {
				logger.Error("recover registry maintenance", "error", err)
				cancel()
				if !waitForNextMaintenance(ctx, ticker) {
					return
				}
				continue
			}
		}
		retentionDue, garbageDue := registry.maintenanceDue(time.Now())
		if !retentionDue && !garbageDue {
			cancel()
			if !waitForNextMaintenance(ctx, ticker) {
				return
			}
			continue
		}
		result, err := s.RunScheduledRegistryMaintenance(passCtx, retentionDue, garbageDue)
		cancel()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, application.ErrRegistryUnavailable) && !errors.Is(err, application.ErrRegistryBusy) {
				logger.Error("automatic registry maintenance failed", "deleted_versions", result.DeletedVersions, "error", err)
			}
		} else if result.RetentionCompleted || result.GarbageCollectionCompleted {
			logger.Info("automatic registry maintenance completed",
				"deleted_versions", result.DeletedVersions,
				"garbage_collected", result.GarbageCollectionCompleted,
				"reclaimed_bytes", result.ReclaimedBytes,
				"reclaimed_bytes_known", result.ReclaimedBytesKnown)
		}
		if !waitForNextMaintenance(ctx, ticker) {
			return
		}
	}
}

func waitForNextMaintenance(ctx context.Context, ticker *time.Ticker) bool {
	select {
	case <-ctx.Done():
		return false
	case <-ticker.C:
		return true
	}
}
