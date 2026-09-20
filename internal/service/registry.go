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

// RegistryService lists the images previously pushed to the managed local
// image registry for the Registry page.
type RegistryService struct {
	container string
	runner    RegistryContentLister
	deleter   RegistryTagDeleter
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

// PlanRepositoryPurge lists one repository's images and splits them into kept
// and purge candidates without deleting anything. The keep count is supplied
// per purge request: keep the N most recent images, 0 for unlimited.
// Protected references (for example currently deployed tags) are always kept
// and do not consume the keep quota.
func (s *RegistryService) PlanRepositoryPurge(ctx context.Context, repository string, keep int, protected map[string]bool) (kept, purge []application.RegistryImage, err error) {
	repository, err = application.ValidateRegistryRepository(repository)
	if err != nil {
		return nil, nil, err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return nil, nil, err
	}
	images, err := s.ListRegistryImages(ctx)
	if err != nil {
		return nil, nil, err
	}
	repositoryImages := make([]application.RegistryImage, 0)
	for _, image := range images {
		if image.Repository == repository {
			repositoryImages = append(repositoryImages, image)
		}
	}
	kept, purge, err = application.PlanRegistryPurge(repositoryImages, keep, protected)
	if err != nil {
		return nil, nil, err
	}
	return kept, purge, nil
}

// PurgeRepository deletes a repository's purge candidates as computed by
// PlanRepositoryPurge for the supplied keep count. Protected references are
// never deleted. It returns the deleted tags in purge order.
func (s *RegistryService) PurgeRepository(ctx context.Context, repository string, keep int, protected map[string]bool) ([]application.RegistryImage, error) {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return nil, err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return nil, err
	}
	if s.deleter == nil {
		return nil, errors.New("registry delete adapter is not configured")
	}
	_, purge, err := s.PlanRepositoryPurge(ctx, repository, keep, protected)
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
