package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"redlaunch/internal/application"
)

// composeServicePuller re-resolves one service's configured image against the
// registry. It is required for version deployments: after a tag move the
// local image must be pulled again before the container can be recreated.
type composeServicePuller interface {
	PullService(context.Context, string, string) error
}

// composeServiceImageResolver returns the effective image reference of one
// Compose service with interpolation applied.
type composeServiceImageResolver interface {
	ResolveServiceImage(context.Context, string, string) (string, error)
}

// composeContainerDigestReader reports the manifest digest behind a running
// container's image.
type composeContainerDigestReader interface {
	ContainerImageDigest(context.Context, string, string) (string, error)
}

// registryVersionSource lists the images previously pushed to the managed
// local image registry.
type registryVersionSource interface {
	ListRegistryImages(context.Context) ([]application.RegistryImage, error)
}

// registryVersionTagMover assigns one registry tag to another image digest
// and returns the previously assigned digest, empty for a new tag.
type registryVersionTagMover interface {
	MoveTag(context.Context, string, string, string) (string, error)
}

// registryVersionTagRemover deletes one registry tag.
type registryVersionTagRemover interface {
	RemoveTag(context.Context, string, string) error
}

// versionRegistryOperations groups the registry capabilities behind version
// deployments. They are supplied through SetVersionRegistryOperations so the
// application service keeps depending on narrow consumer-side interfaces.
type versionRegistryOperations struct {
	images  registryVersionSource
	mover   registryVersionTagMover
	remover registryVersionTagRemover
}

// SetVersionRegistryOperations supplies the registry listing and tag
// operations used by version deployments.
func (s *Applications) SetVersionRegistryOperations(images registryVersionSource, mover registryVersionTagMover, remover registryVersionTagRemover) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionRegistry = versionRegistryOperations{images: images, mover: mover, remover: remover}
}

func (s *Applications) versionOperations() versionRegistryOperations {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versionRegistry
}

// serviceVersionDeployment is one service's resolved version deployment
// identity: the local-registry repository and deployment tag its container
// deploys from.
type serviceVersionDeployment struct {
	directory      string
	serviceName    string
	repository     string
	imageReference string
	deploymentTag  string
	containerName  string
}

// serviceVersionDeployment resolves the service's effective image reference
// exactly as Compose applies it, falling back to the registered image when
// the Compose configuration cannot be read. Services that do not deploy their
// image through the managed local registry report
// application.ErrServiceVersionUnavailable.
func (s *Applications) serviceVersionDeployment(ctx context.Context, applicationID int64, serviceName string) (serviceVersionDeployment, error) {
	serviceName, err := application.ValidateServiceName(serviceName)
	if err != nil {
		return serviceVersionDeployment{}, err
	}
	if s.detailsRepository == nil {
		return serviceVersionDeployment{}, errors.New("application details repository is not configured")
	}
	item, err := s.detailsRepository.Get(ctx, applicationID)
	if err != nil {
		return serviceVersionDeployment{}, err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return serviceVersionDeployment{}, err
	}
	registered := false
	var service application.Service
	for _, candidate := range services {
		if candidate.Name == serviceName {
			registered = true
			service = candidate
			break
		}
	}
	if !registered {
		return serviceVersionDeployment{}, application.ErrServiceNotFound
	}
	directory, err := s.managedApplicationDirectory(item)
	if err != nil {
		return serviceVersionDeployment{}, err
	}

	imageName := strings.TrimSpace(service.ImageName)
	if resolver, ok := s.runner.(composeServiceImageResolver); ok {
		if resolved, resolveErr := resolver.ResolveServiceImage(ctx, directory, serviceName); resolveErr == nil && strings.TrimSpace(resolved) != "" {
			imageName = strings.TrimSpace(resolved)
		}
	}
	repository, tag, ok := application.ParseDeployedRegistryReference(imageName)
	if !ok {
		return serviceVersionDeployment{}, application.ErrServiceVersionUnavailable
	}

	deployment := serviceVersionDeployment{
		directory:      directory,
		serviceName:    serviceName,
		repository:     repository,
		imageReference: application.LocalRegistryAddress + "/" + repository,
		deploymentTag:  tag,
	}
	if inspector, ok := s.runner.(composeServiceInspector); ok {
		if runtimeServices, listErr := inspector.ListServices(ctx, directory); listErr == nil {
			for _, runtimeService := range runtimeServices {
				if runtimeService.ServiceName == serviceName {
					deployment.containerName = runtimeService.ContainerName
					break
				}
			}
		}
	}
	return deployment, nil
}

// ListServiceVersions returns the pushed registry images for one service's
// repository together with the deployed and running image state. Registry
// failures only clear the availability flag so the page still renders the
// service's deployment identity.
func (s *Applications) ListServiceVersions(ctx context.Context, applicationID int64, serviceName string) (application.ServiceVersions, error) {
	deployment, err := s.serviceVersionDeployment(ctx, applicationID, serviceName)
	if err != nil {
		return application.ServiceVersions{}, err
	}
	versions := application.ServiceVersions{
		ServiceName:   deployment.serviceName,
		Repository:    deployment.repository,
		DeploymentTag: deployment.deploymentTag,
	}
	operations := s.versionOperations()
	if operations.images == nil {
		return versions, nil
	}
	images, err := operations.images.ListRegistryImages(ctx)
	if err != nil {
		return versions, nil
	}
	versions.RegistryAvailable = true
	versions.Versions = application.GroupRegistryVersions(images, deployment.repository)
	versions.TaggedDigest = application.DigestForTag(versions.Versions, deployment.deploymentTag)
	if deployment.containerName != "" {
		if reader, ok := s.runner.(composeContainerDigestReader); ok {
			if digest, digestErr := reader.ContainerImageDigest(ctx, deployment.containerName, deployment.imageReference); digestErr == nil {
				versions.RunningDigest = digest
			}
		}
	}
	return versions, nil
}

// DeployServiceVersion moves one service's deployment tag to the selected
// image digest, pulls the image again and recreates the service container.
// When the pull or recreation fails the tag is moved back to its previous
// target so the registry never disagrees with the running service.
func (s *Applications) DeployServiceVersion(ctx context.Context, applicationID int64, serviceName, digest string, progress func(stage, message string)) error {
	digest, err := application.ValidateRegistryDigest(digest)
	if err != nil {
		return err
	}
	operations := s.versionOperations()
	if operations.mover == nil {
		return errors.New("registry tag operations are not configured")
	}

	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return err
	}
	defer lease.release()

	deployment, err := s.serviceVersionDeployment(ctx, applicationID, serviceName)
	if err != nil {
		return err
	}
	report := func(stage, message string) {
		if progress != nil {
			progress(stage, message)
		}
	}
	report("tag", "Assigning "+deployment.deploymentTag+" to the selected image")
	previous, err := operations.mover.MoveTag(ctx, deployment.repository, deployment.deploymentTag, digest)
	if err != nil {
		return fmt.Errorf("assign registry tag: %w", err)
	}
	if err := s.deployVersionImage(ctx, deployment, report); err != nil {
		if rollbackErr := s.restoreVersionTag(ctx, deployment, previous); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore registry tag: %w", rollbackErr))
		}
		return err
	}
	return nil
}

// deployVersionImage pulls the moved tag and recreates the service container.
func (s *Applications) deployVersionImage(ctx context.Context, deployment serviceVersionDeployment, report func(stage, message string)) error {
	puller, ok := s.runner.(composeServicePuller)
	if !ok {
		return errors.New("service image pull is not configured")
	}
	report("pull", "Pulling the selected image")
	if err := puller.PullService(ctx, deployment.directory, deployment.serviceName); err != nil {
		return fmt.Errorf("pull service image: %w", err)
	}
	report("deploy", "Recreating the service container")
	if err := s.startManagedService(ctx, deployment.directory, deployment.serviceName); err != nil {
		return fmt.Errorf("recreate service: %w", err)
	}
	return nil
}

// restoreVersionTag returns a deployment tag to its previous target after a
// failed deployment. A tag that did not exist before is removed again.
func (s *Applications) restoreVersionTag(ctx context.Context, deployment serviceVersionDeployment, previous string) error {
	operations := s.versionOperations()
	if previous != "" {
		if operations.mover == nil {
			return errors.New("registry tag operations are not configured")
		}
		_, err := operations.mover.MoveTag(ctx, deployment.repository, deployment.deploymentTag, previous)
		return err
	}
	if operations.remover == nil {
		return errors.New("registry tag operations are not configured")
	}
	return operations.remover.RemoveTag(ctx, deployment.repository, deployment.deploymentTag)
}

// ListApplicationVersions returns the services that deploy local-registry
// images together with the tags available across their repositories. It
// serves the application-wide version deployment dialog and reads only
// registered metadata and the registry listing.
func (s *Applications) ListApplicationVersions(ctx context.Context, applicationID int64) (application.ApplicationVersions, error) {
	if s.detailsRepository == nil {
		return application.ApplicationVersions{}, errors.New("application details repository is not configured")
	}
	if _, err := s.detailsRepository.Get(ctx, applicationID); err != nil {
		return application.ApplicationVersions{}, err
	}
	services, err := s.detailsRepository.ListServices(ctx, applicationID)
	if err != nil {
		return application.ApplicationVersions{}, err
	}
	result := application.ApplicationVersions{Services: []application.ServiceVersionTarget{}}
	for _, service := range services {
		repository, tag, ok := application.ParseDeployedRegistryReference(service.ImageName)
		if !ok {
			continue
		}
		result.Services = append(result.Services, application.ServiceVersionTarget{
			ServiceName:   service.Name,
			Repository:    repository,
			DeploymentTag: tag,
		})
	}
	operations := s.versionOperations()
	if operations.images == nil || len(result.Services) == 0 {
		return result, nil
	}
	images, err := operations.images.ListRegistryImages(ctx)
	if err != nil {
		return result, nil
	}
	result.Tags = application.GroupApplicationVersionTags(images, result.Services)
	return result, nil
}

// DeployApplicationVersion moves each eligible service's deployment tag to
// the image tagged sourceTag in that service's own repository and recreates
// the service. Services whose repository does not contain the tag are
// skipped; a failing service does not stop the remaining services. Outcomes
// are reported per service in registration order.
func (s *Applications) DeployApplicationVersion(ctx context.Context, applicationID int64, sourceTag string, progress func(serviceName, state, message string)) ([]application.VersionDeployOutcome, error) {
	sourceTag = strings.TrimSpace(sourceTag)
	if sourceTag == "" {
		return nil, application.ErrVersionTagRequired
	}
	if _, err := application.ValidateRegistryTag(sourceTag); err != nil {
		return nil, err
	}
	operations := s.versionOperations()
	if operations.images == nil || operations.mover == nil {
		return nil, errors.New("registry tag operations are not configured")
	}

	lease, err := s.acquireApplicationProject(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

	versions, err := s.ListApplicationVersions(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if len(versions.Services) == 0 {
		return nil, application.ErrServiceVersionUnavailable
	}
	images, err := operations.images.ListRegistryImages(ctx)
	if err != nil {
		return nil, err
	}

	outcomes := make([]application.VersionDeployOutcome, 0, len(versions.Services))
	for _, target := range versions.Services {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		outcome := application.VersionDeployOutcome{ServiceName: target.ServiceName}
		report := func(state, message string) {
			if progress != nil {
				progress(target.ServiceName, state, message)
			}
		}
		report(application.VersionDeployStateActive, "Deploying "+sourceTag)

		deployment, err := s.serviceVersionDeployment(ctx, applicationID, target.ServiceName)
		if err != nil {
			outcome.State = application.VersionDeployStateFailed
			outcome.Detail = "The service deployment could not be resolved."
			report(outcome.State, outcome.Detail)
			outcomes = append(outcomes, outcome)
			continue
		}
		digest := application.DigestForTag(application.GroupRegistryVersions(images, deployment.repository), sourceTag)
		if digest == "" {
			outcome.State = application.VersionDeployStateSkipped
			outcome.Detail = "The repository " + deployment.repository + " has no tag " + sourceTag + "."
			report(outcome.State, outcome.Detail)
			outcomes = append(outcomes, outcome)
			continue
		}
		outcome.State, outcome.Detail = s.deployVersionTarget(ctx, deployment, digest, sourceTag, report)
		report(outcome.State, outcome.Detail)
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

// deployVersionTarget performs one service's tag move, pull and recreation
// and maps failures onto a short operator-facing outcome detail.
func (s *Applications) deployVersionTarget(ctx context.Context, deployment serviceVersionDeployment, digest, sourceTag string, report func(state, message string)) (string, string) {
	operations := s.versionOperations()
	stageReport := func(stage, message string) {
		report(application.VersionDeployStateActive, message)
	}
	previous, err := operations.mover.MoveTag(ctx, deployment.repository, deployment.deploymentTag, digest)
	if err != nil {
		return application.VersionDeployStateFailed, "The deployment tag could not be assigned."
	}
	if err := s.deployVersionImage(ctx, deployment, stageReport); err != nil {
		if rollbackErr := s.restoreVersionTag(ctx, deployment, previous); rollbackErr != nil {
			return application.VersionDeployStateFailed, "The deployment failed and the deployment tag could not be restored."
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return application.VersionDeployStateFailed, "The deployment was interrupted and the deployment tag was restored."
		}
		return application.VersionDeployStateFailed, "The deployment failed and the deployment tag was restored."
	}
	return application.VersionDeployStateDeployed, "The deployment tag now points at " + sourceTag + "."
}
