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

// RegistryTagMover assigns one registry tag to another image digest and
// returns the digest the tag was assigned to before, empty for a new tag.
type RegistryTagMover interface {
	MoveRegistryTag(context.Context, string, string, string, string) (string, error)
}

// RegistryService lists the images previously pushed to the managed local
// image registry for the Registry page.
type RegistryService struct {
	container string
	runner    RegistryContentLister
	deleter   RegistryTagDeleter
	mover     RegistryTagMover
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
	if mover, ok := runner.(RegistryTagMover); ok {
		service.mover = mover
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

// SetTagMover overrides the tag move adapter, primarily for tests.
func (s *RegistryService) SetTagMover(mover RegistryTagMover) {
	if s == nil {
		return
	}
	s.mover = mover
}

// ListRegistryImages returns the tags previously pushed to the managed
// local image registry in a stable order. A registry container that cannot
// be reached reports application.ErrRegistryUnavailable so the Registry
// page can distinguish a stopped registry from a failed inspection.
func (s *RegistryService) ListRegistryImages(ctx context.Context) ([]application.RegistryImage, error) {
	return s.listRegistryImages(ctx, false)
}

// ListRegistryVersionImages includes retained manifests whose last tag moved away.
// The Registry page and tag retention continue to use ListRegistryImages.
func (s *RegistryService) ListRegistryVersionImages(ctx context.Context) ([]application.RegistryImage, error) {
	return s.listRegistryImages(ctx, true)
}

func (s *RegistryService) listRegistryImages(ctx context.Context, includeUntagged bool) ([]application.RegistryImage, error) {
	contents, err := s.runner.ListRegistryContents(ctx, s.container)
	if err != nil {
		if errors.Is(err, compose.ErrRegistryUnavailable) {
			return nil, application.ErrRegistryUnavailable
		}
		return nil, fmt.Errorf("list registry contents: %w", err)
	}
	images := make([]application.RegistryImage, 0, len(contents))
	for _, content := range contents {
		if content.Tag == "" && !includeUntagged {
			continue
		}
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

// MoveTag assigns one repository tag to the given manifest digest and
// returns the digest the tag was assigned to before the move, empty when the
// tag did not exist yet. The digest must be a manifest previously pushed to
// the same repository.
func (s *RegistryService) MoveTag(ctx context.Context, repository, tag, digest string) (string, error) {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return "", err
	}
	tag, err = application.ValidateRegistryTag(tag)
	if err != nil {
		return "", err
	}
	digest, err = application.ValidateRegistryDigest(digest)
	if err != nil {
		return "", err
	}
	if s.mover == nil {
		return "", errors.New("registry tag move adapter is not configured")
	}
	previous, err := s.mover.MoveRegistryTag(ctx, s.container, repository, tag, digest)
	if err != nil {
		if errors.Is(err, compose.ErrRegistryImageAbsent) {
			return previous, application.ErrVersionImageAbsent
		}
		return previous, fmt.Errorf("move registry tag %s:%s: %w", repository, tag, err)
	}
	return previous, nil
}

// RemoveTag deletes one repository tag. It exists to undo a first-time tag
// assignment when a version deployment fails before the service was
// recreated.
func (s *RegistryService) RemoveTag(ctx context.Context, repository, tag string) error {
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return err
	}
	tag, err = application.ValidateRegistryTag(tag)
	if err != nil {
		return err
	}
	if s.deleter == nil {
		return errors.New("registry delete adapter is not configured")
	}
	if err := s.deleter.DeleteRegistryTag(ctx, s.container, repository, tag); err != nil {
		return fmt.Errorf("remove registry tag %s:%s: %w", repository, tag, err)
	}
	return nil
}
