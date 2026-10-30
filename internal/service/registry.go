package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	mutations sync.Mutex
	runner    RegistryContentLister
	deleter   RegistryTagDeleter
	mover     RegistryTagMover

	retentionStore RegistryRetentionStore
	stateDir       string

	scheduleMu     sync.Mutex
	lastRetention  time.Time
	lastGarbage    time.Time
	maintenanceErr error
}

// RegistryRetentionStore persists the shared automatic/manual retention
// policy. Zero means unlimited.
type RegistryRetentionStore interface {
	GetRegistryRetentionKeep(context.Context) (int, error)
	SetRegistryRetentionKeep(context.Context, int) error
	ListRegistryRetentionPolicies(context.Context) ([]application.RegistryRetentionPolicy, error)
	SetRegistryRetentionPolicy(context.Context, string, int) error
	DeleteRegistryRetentionPolicy(context.Context, string) error
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
		pushedAt := content.PushedAt
		if includeUntagged {
			pushedAt = content.ManifestPushedAt
		}
		images = append(images, application.RegistryImage{
			Repository: content.Repository,
			Tag:        content.Tag,
			Digest:     content.Digest,
			PushedAt:   pushedAt,
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

// MoveTag assigns one repository tag to the given manifest digest and
// returns the digest the tag was assigned to before the move, empty when the
// tag did not exist yet. The digest must be a manifest previously pushed to
// the same repository.
func (s *RegistryService) MoveTag(ctx context.Context, repository, tag, digest string) (string, error) {
	s.mutations.Lock()
	defer s.mutations.Unlock()
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
	s.mutations.Lock()
	defer s.mutations.Unlock()
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
