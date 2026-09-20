package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

// RegistryContainerName is the managed local image registry container that
// receives GitHub Actions workflow pushes through the SSH tunnel. It must
// match the container_name in the registry Compose project written by setup.
const RegistryContainerName = "redbolt-registry"

// RegistryContentLister reads the tags previously pushed to the managed
// local image registry container.
type RegistryContentLister interface {
	ListRegistryContents(context.Context, string) ([]compose.RegistryContent, error)
}

// RegistryTagDeleter removes one tag from the managed registry container.
type RegistryTagDeleter interface {
	DeleteRegistryTag(context.Context, string, string, string) error
}

// RegistryRetentionStore persists the global default and per-repository keep
// overrides for registry retention.
type RegistryRetentionStore interface {
	GetRegistryDefaultKeep(context.Context) (int, error)
	SetRegistryDefaultKeep(context.Context, int) error
	ListRegistryRetentionPolicies(context.Context) ([]application.RegistryRetentionPolicy, error)
	GetRegistryRetentionPolicy(context.Context, string) (int, bool, error)
	SetRegistryRetentionPolicy(context.Context, string, int) error
	DeleteRegistryRetentionPolicy(context.Context, string) error
}

// RegistryService lists the images previously pushed to the managed local
// image registry for the Registry page.
type RegistryService struct {
	container string
	runner    RegistryContentLister
	deleter   RegistryTagDeleter
	retention RegistryRetentionStore
}

// NewRegistryService constructs a registry service for the given managed
// registry container.
func NewRegistryService(container string, runner RegistryContentLister) (*RegistryService, error) {
	if strings.TrimSpace(container) == "" {
		return nil, errors.New("registry container name is required")
	}
	if runner == nil {
		runner = compose.CommandRunner{}
	}
	service := &RegistryService{
		container: container,
		runner:    runner,
	}
	if deleter, ok := runner.(RegistryTagDeleter); ok {
		service.deleter = deleter
	}
	return service, nil
}

// SetRetentionStore supplies the SQLite-backed retention policies. It is
// optional so existing callers and tests without a database keep working for
// read-only listing.
func (s *RegistryService) SetRetentionStore(store RegistryRetentionStore) {
	if s == nil {
		return
	}
	s.retention = store
}

// SetTagDeleter overrides the delete adapter, primarily for tests.
func (s *RegistryService) SetTagDeleter(deleter RegistryTagDeleter) {
	if s == nil {
		return
	}
	s.deleter = deleter
}

// ListRegistryImages returns the tags previously pushed to the managed
// local image registry in a stable order. A registry container that cannot
// be reached reports application.ErrRegistryUnavailable so the Registry
// page can distinguish a stopped registry from a failed inspection.
func (s *RegistryService) ListRegistryImages(ctx context.Context) ([]application.RegistryImage, error) {
	contents, err := s.runner.ListRegistryContents(ctx, s.container)
	if err != nil {
		if errors.Is(err, compose.ErrRegistryUnavailable) {
			return nil, application.ErrRegistryUnavailable
		}
		return nil, fmt.Errorf("list registry contents: %w", err)
	}
	images := make([]application.RegistryImage, 0, len(contents))
	for _, content := range contents {
		images = append(images, application.RegistryImage{
			Repository: content.Repository,
			Tag:        content.Tag,
			Digest:     content.Digest,
			PushedAt:   content.PushedAt,
		})
	}
	sort.Slice(images, func(left, right int) bool {
		if images[left].Repository != images[right].Repository {
			return images[left].Repository < images[right].Repository
		}
		if images[left].Tag != images[right].Tag {
			return images[left].Tag < images[right].Tag
		}
		return images[left].Digest < images[right].Digest
	})
	return images, nil
}

// GetDefaultKeep returns the global retention default, falling back to the
// application default when no store is configured.
func (s *RegistryService) GetDefaultKeep(ctx context.Context) (int, error) {
	if s.retention == nil {
		return application.DefaultRegistryKeepCount, nil
	}
	return s.retention.GetRegistryDefaultKeep(ctx)
}

// SetDefaultKeep persists the global retention default. Zero means unlimited.
func (s *RegistryService) SetDefaultKeep(ctx context.Context, keep int) error {
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	if s.retention == nil {
		return errors.New("registry retention store is not configured")
	}
	return s.retention.SetRegistryDefaultKeep(ctx, keep)
}

// ListRetentionPolicies returns per-repository overrides in repository order.
func (s *RegistryService) ListRetentionPolicies(ctx context.Context) ([]application.RegistryRetentionPolicy, error) {
	if s.retention == nil {
		return nil, nil
	}
	return s.retention.ListRegistryRetentionPolicies(ctx)
}

// SetRepositoryKeep creates or updates one repository override. Zero means
// unlimited for that repository.
func (s *RegistryService) SetRepositoryKeep(ctx context.Context, repository string, keep int) error {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	if s.retention == nil {
		return errors.New("registry retention store is not configured")
	}
	return s.retention.SetRegistryRetentionPolicy(ctx, repository, keep)
}

// DeleteRepositoryKeep removes one repository override so the global default
// applies again.
func (s *RegistryService) DeleteRepositoryKeep(ctx context.Context, repository string) error {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return err
	}
	if s.retention == nil {
		return errors.New("registry retention store is not configured")
	}
	return s.retention.DeleteRegistryRetentionPolicy(ctx, repository)
}

// effectiveKeepFor resolves the keep count for one repository.
func (s *RegistryService) effectiveKeepFor(ctx context.Context, repository string) (int, error) {
	defaultKeep := application.DefaultRegistryKeepCount
	policies := map[string]int{}
	if s.retention != nil {
		storedDefault, err := s.retention.GetRegistryDefaultKeep(ctx)
		if err != nil {
			return 0, err
		}
		defaultKeep = storedDefault
		stored, err := s.retention.ListRegistryRetentionPolicies(ctx)
		if err != nil {
			return 0, err
		}
		for _, policy := range stored {
			policies[policy.Repository] = policy.KeepCount
		}
	}
	return application.EffectiveRegistryKeep(defaultKeep, policies, repository), nil
}

// PlanRepositoryPurge lists one repository's images and splits them into kept
// and purge candidates without deleting anything. Protected references (for
// example currently deployed tags) are always kept.
func (s *RegistryService) PlanRepositoryPurge(ctx context.Context, repository string, protected map[string]bool) (kept, purge []application.RegistryImage, effectiveKeep int, err error) {
	repository, err = application.ValidateRegistryRepository(repository)
	if err != nil {
		return nil, nil, 0, err
	}
	images, err := s.ListRegistryImages(ctx)
	if err != nil {
		return nil, nil, 0, err
	}
	repositoryImages := make([]application.RegistryImage, 0)
	for _, image := range images {
		if image.Repository == repository {
			repositoryImages = append(repositoryImages, image)
		}
	}
	effectiveKeep, err = s.effectiveKeepFor(ctx, repository)
	if err != nil {
		return nil, nil, 0, err
	}
	kept, purge, err = application.PlanRegistryPurge(repositoryImages, effectiveKeep, protected)
	if err != nil {
		return nil, nil, 0, err
	}
	return kept, purge, effectiveKeep, nil
}

// PurgeRepository deletes a repository's purge candidates as computed by
// PlanRepositoryPurge. Protected references are never deleted. It returns the
// deleted tags in purge order.
func (s *RegistryService) PurgeRepository(ctx context.Context, repository string, protected map[string]bool) ([]application.RegistryImage, error) {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return nil, err
	}
	if s.deleter == nil {
		return nil, errors.New("registry delete adapter is not configured")
	}
	_, purge, _, err := s.PlanRepositoryPurge(ctx, repository, protected)
	if err != nil {
		return nil, err
	}
	purged := make([]application.RegistryImage, 0, len(purge))
	for _, image := range purge {
		if err := s.deleter.DeleteRegistryTag(ctx, s.container, image.Repository, image.Tag); err != nil {
			return purged, fmt.Errorf("delete registry tag %s:%s: %w", image.Repository, image.Tag, err)
		}
		purged = append(purged, image)
	}
	return purged, nil
}
