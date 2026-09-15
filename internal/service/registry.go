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

// RegistryService lists the images previously pushed to the managed local
// image registry for the Registry page.
type RegistryService struct {
	container string
	runner    RegistryContentLister
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
	return &RegistryService{
		container: container,
		runner:    runner,
	}, nil
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
