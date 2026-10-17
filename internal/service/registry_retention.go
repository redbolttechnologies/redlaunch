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

// RegistryRetentionKeepCount is the automatic, per-repository version limit.
// Older deployed versions are retained in addition to this limit.
const RegistryRetentionKeepCount = 2

// RegistryVersionStorage operates on manifests, independently of their tags.
// References returns the child manifests of an image index (if any).
type RegistryVersionStorage interface {
	RegistryManifestReferences(context.Context, string, string, string) ([]string, error)
	DeleteRegistryVersion(context.Context, string, string, string) error
}

// RetainVersions keeps the newest distinct versions of every repository.
// Protected keys identify repository/digest pairs or repository/tag pairs.
// All manifests are inspected before any deletion, so an incomplete image
// graph can never cause a multi-platform image's children to be deleted.
func (s *RegistryService) RetainVersions(ctx context.Context, protected map[string]bool) (int, error) {
	s.mutations.Lock()
	defer s.mutations.Unlock()
	storage, ok := s.runner.(RegistryVersionStorage)
	if !ok {
		return 0, errors.New("registry version storage is not configured")
	}
	images, err := s.ListRegistryVersionImages(ctx)
	if err != nil {
		return 0, err
	}
	repositories := make(map[string]bool)
	for _, image := range images {
		repositories[image.Repository] = true
	}
	names := make([]string, 0, len(repositories))
	for name := range repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	type candidate struct{ repository, digest string }
	var purge []candidate
	for _, repository := range names {
		versions := application.GroupRegistryVersions(images, repository)
		references := make(map[string][]string)
		children := make(map[string]bool)
		keep := make(map[string]bool)
		for _, version := range versions {
			refs, err := storage.RegistryManifestReferences(ctx, s.container, repository, version.Digest)
			if err != nil {
				return 0, fmt.Errorf("inspect registry manifest: %w", err)
			}
			references[version.Digest] = refs
			for _, digest := range refs {
				children[digest] = true
			}
			if version.PushedAt.IsZero() || protected[application.RegistryImageKey(repository, version.Digest)] {
				keep[version.Digest] = true
			}
			for _, tag := range version.Tags {
				if protected[application.RegistryImageKey(repository, tag)] {
					keep[version.Digest] = true
				}
			}
		}
		for _, refs := range references {
			for _, digest := range refs {
				if _, exists := references[digest]; !exists {
					return 0, errors.New("registry manifest graph is incomplete")
				}
			}
		}
		// Child manifests of an index are implementation details of that image,
		// not additional versions. Tagged children can also be standalone versions.
		count := 0
		for _, version := range versions {
			if children[version.Digest] && len(version.Tags) == 0 {
				continue
			}
			if count < RegistryRetentionKeepCount {
				keep[version.Digest] = true
				count++
			}
		}
		var retainChildren func(string)
		visited := make(map[string]bool)
		retainChildren = func(digest string) {
			if visited[digest] {
				return
			}
			visited[digest] = true
			keep[digest] = true
			for _, child := range references[digest] {
				retainChildren(child)
			}
		}
		for digest := range keep {
			retainChildren(digest)
		}
		// Remove obsolete indexes before their children. If a later deletion
		// fails, the remaining inventory still has a complete manifest graph.
		parents := make(map[string][]string)
		for _, version := range versions {
			for _, child := range references[version.Digest] {
				parents[child] = append(parents[child], version.Digest)
			}
		}
		ordered := make(map[string]bool)
		visiting := make(map[string]bool)
		var orderDeletion func(string) error
		orderDeletion = func(digest string) error {
			if keep[digest] || ordered[digest] {
				return nil
			}
			if visiting[digest] {
				return errors.New("registry manifest graph contains a cycle")
			}
			visiting[digest] = true
			for _, parent := range parents[digest] {
				if err := orderDeletion(parent); err != nil {
					return err
				}
			}
			visiting[digest] = false
			ordered[digest] = true
			purge = append(purge, candidate{repository, digest})
			return nil
		}
		for _, version := range versions {
			if err := orderDeletion(version.Digest); err != nil {
				return 0, err
			}
		}
	}
	deleted := 0
	for _, version := range purge {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := storage.DeleteRegistryVersion(ctx, s.container, version.repository, version.digest); err != nil {
			return deleted, fmt.Errorf("delete retained registry version: %w", err)
		}
		deleted++
	}
	return deleted, nil
}

// RetainRegistryVersions holds the application project leases throughout
// inspection and cleanup so deployment, rollback, and deletion cannot race
// retention. Failed runtime inspection aborts cleanup rather than guessing
// which image a container uses.
func (s *Applications) RetainRegistryVersions(ctx context.Context, registry *RegistryService) (int, error) {
	items, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, item := range items {
		lease, err := s.acquireApplicationProject(ctx, item.ID)
		if err != nil {
			return 0, err
		}
		defer lease.release()
	}
	protected := make(map[string]bool)
	inspector, ok := s.runner.(composeServiceInspector)
	if !ok {
		return 0, errors.New("container inspection is not configured")
	}
	resolver, ok := s.runner.(composeServiceImageResolver)
	if !ok {
		return 0, errors.New("service image resolution is not configured")
	}
	reader, ok := s.runner.(composeContainerDigestReader)
	if !ok {
		return 0, errors.New("container image inspection is not configured")
	}
	for _, item := range items {
		services, err := s.detailsRepository.ListServices(ctx, item.ID)
		if err != nil {
			return 0, err
		}
		directory, err := s.managedApplicationDirectory(item)
		if err != nil {
			return 0, err
		}
		runtime, err := inspector.ListServices(ctx, directory)
		if err != nil {
			return 0, fmt.Errorf("inspect services before registry retention: %w", err)
		}
		for _, container := range runtime {
			reference, local := strings.CutPrefix(container.Image, application.LocalRegistryAddress+"/")
			if !local || container.ContainerName == "" {
				continue
			}
			repository := strings.SplitN(strings.SplitN(reference, "@", 2)[0], ":", 2)[0]
			if _, err := application.ValidateRegistryRepository(repository); err != nil {
				return 0, err
			}
			digest, err := reader.ContainerImageDigest(ctx, container.ContainerName, application.LocalRegistryAddress+"/"+repository)
			if err != nil || digest == "" {
				return 0, errors.New("runtime image could not be verified before registry retention")
			}
			protected[application.RegistryImageKey(repository, digest)] = true
		}
		for _, service := range services {
			image, err := resolver.ResolveServiceImage(ctx, directory, service.Name)
			if err != nil {
				return 0, fmt.Errorf("resolve service image before registry retention: %w", err)
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
					return 0, err
				}
				if _, err := application.ValidateRegistryDigest(digest); err != nil {
					return 0, err
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
				return 0, errors.New("invalid local registry service reference")
			}
			protected[application.RegistryImageKey(repository, tag)] = true
			for _, container := range runtime {
				if container.ServiceName != service.Name || container.ContainerName == "" {
					continue
				}
				digest, err := reader.ContainerImageDigest(ctx, container.ContainerName, application.LocalRegistryAddress+"/"+repository)
				if err != nil || digest == "" {
					return 0, errors.New("deployed image could not be verified before registry retention")
				}
				protected[application.RegistryImageKey(repository, digest)] = true
			}
		}
	}
	return registry.RetainVersions(ctx, protected)
}

// RunRegistryRetention runs once at startup and hourly while Redlaunch is
// serving. Each pass is bounded and cancellation stops in-flight Docker work.
func (s *Applications) RunRegistryRetention(ctx context.Context, registry *RegistryService, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		deleted, err := s.RetainRegistryVersions(passCtx, registry)
		cancel()
		if err != nil && ctx.Err() == nil && !errors.Is(err, application.ErrRegistryUnavailable) {
			logger.Error("automatic registry retention failed", "deleted_versions", deleted, "error", err)
		} else if deleted > 0 {
			logger.Info("automatic registry retention completed", "deleted_versions", deleted, "keep_versions", RegistryRetentionKeepCount)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
