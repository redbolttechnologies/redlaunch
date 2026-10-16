package service

import (
	"context"
	"errors"
	"fmt"

	"redlaunch/internal/application"
)

type serviceVolumeRunner interface {
	ListServiceDeletionVolumes(context.Context, string, string) ([]application.ServiceDeletionVolume, error)
	RemoveServiceVolumes(context.Context, string, string, []string) error
}

type serviceDeletionVolumeRepository interface {
	SetServiceDeletionVolumes(context.Context, int64, string, []string) error
}

func (s *Applications) ListServiceDeletionVolumes(ctx context.Context, id int64, name string) ([]application.ServiceDeletionVolume, error) {
	if s.detailsRepository == nil {
		return nil, errors.New("application details repository is not configured")
	}
	name, err := application.ValidateServiceName(name)
	if err != nil {
		return nil, err
	}
	lease, err := s.acquireApplicationProject(ctx, id)
	if err != nil {
		return nil, err
	}
	defer lease.release()
	if err := s.ensureApplicationNotDeleting(ctx, id); err != nil {
		return nil, err
	}
	item, err := s.detailsRepository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	services, err := s.detailsRepository.ListServices(ctx, id)
	if err != nil {
		return nil, err
	}
	found := false
	for _, svc := range services {
		if svc.Name == name {
			found = true
			break
		}
	}
	if !found {
		return nil, application.ErrServiceNotFound
	}
	if s.serviceDeletionIntents != nil {
		intent, err := s.serviceDeletionIntents.GetServiceDeletion(ctx, id, name)
		if err != nil && !errors.Is(err, application.ErrServiceNotFound) {
			return nil, err
		}
		if err == nil && intent.State != "complete" {
			// Retrying cannot silently change the original destructive choices.
			volumes := make([]application.ServiceDeletionVolume, 0, len(intent.Volumes))
			for _, name := range intent.Volumes {
				volumes = append(volumes, application.ServiceDeletionVolume{Name: name, Selected: true, Frozen: true})
			}
			return volumes, nil
		}
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return nil, err
	}
	runner, ok := s.runner.(serviceVolumeRunner)
	if !ok {
		return nil, errors.New("service volume management is not configured")
	}
	return runner.ListServiceDeletionVolumes(ctx, directory, name)
}

func (s *Applications) validateDeletionVolumes(ctx context.Context, directory, service string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if len(names) > 128 {
		return errors.New("too many selected volumes")
	}
	runner, ok := s.runner.(serviceVolumeRunner)
	if !ok {
		return errors.New("service volume management is not configured")
	}
	volumes, err := runner.ListServiceDeletionVolumes(ctx, directory, service)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, v := range volumes {
		allowed[v.Name] = true
	}
	for _, name := range names {
		if !allowed[name] {
			return errors.New("selected volume is not eligible for deletion")
		}
		delete(allowed, name)
	}
	return nil
}

func (s *Applications) removeDeletionVolumes(ctx context.Context, directory, service string, names []string, progress func(string, string)) error {
	if len(names) == 0 {
		return nil
	}
	reportServiceDeletionProgress(progress, "volumes", "Deleting selected service volumes")
	runner, ok := s.runner.(serviceVolumeRunner)
	if !ok {
		return errors.New("service volume management is not configured")
	}
	if err := runner.RemoveServiceVolumes(ctx, directory, service, names); err != nil {
		return fmt.Errorf("delete service volumes: %w", err)
	}
	return nil
}
