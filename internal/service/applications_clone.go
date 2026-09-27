package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

// cloneExtraFileBudget caps the optional extra-file copy so a large
// bind-mounted data directory cannot fill the disk by accident. Managed files
// (compose.yml, vars.env, secrets.env and scoped service files) are always
// copied and are bounded by their own validation limits.
const (
	cloneExtraFileMaxBytes   = 64 << 20
	cloneExtraFileMaxPerFile = 16 << 20
)

// CloneApplication copies an existing application into a new, independent
// application. It is the environments workflow (dev, stage, production): the
// clone gets its own folder, Compose project, containers, volumes and
// metadata, and is always left stopped.
//
// Secrets and variables are copied as-is and must be reviewed before the
// first start; database passwords in particular are read only on volume
// initialization. Domains are copied without routings so the operator
// re-maps routing manually. Volume data, backup history, API tokens and
// deletion intents are never copied.
func (s *Applications) CloneApplication(ctx context.Context, sourceID int64, input application.CloneInput) (application.Application, error) {
	name, err := application.ValidateName(input.Name)
	if err != nil {
		return application.Application{}, err
	}
	folderName, err := application.ValidateFolderName(input.FolderName)
	if err != nil {
		return application.Application{}, err
	}
	if s.detailsRepository == nil {
		return application.Application{}, errors.New("application details repository is not configured")
	}
	if s.serviceRepository == nil {
		return application.Application{}, errors.New("application service repository is not configured")
	}

	folderLease, err := s.acquireProjectKey(ctx, applicationFolderLockKey(folderName))
	if err != nil {
		return application.Application{}, err
	}
	defer folderLease.release()

	if err := s.ensureFolderNotReserved(ctx, folderName); err != nil {
		return application.Application{}, err
	}

	sourceLease, err := s.acquireApplicationProject(ctx, sourceID)
	if err != nil {
		return application.Application{}, err
	}
	defer sourceLease.release()

	if err := s.ensureApplicationNotDeleting(ctx, sourceID); err != nil {
		return application.Application{}, err
	}

	source, err := s.detailsRepository.Get(ctx, sourceID)
	if err != nil {
		return application.Application{}, err
	}
	sourceDirectory, err := s.managedApplicationDirectory(source)
	if err != nil {
		return application.Application{}, err
	}
	sourceComposePath, err := findApplicationComposeFile(sourceDirectory)
	if err != nil {
		return application.Application{}, fmt.Errorf("find source application Compose file: %w", err)
	}
	if sourceComposePath == "" {
		return application.Application{}, errors.New("source application Compose file does not exist")
	}

	if err := ensureDirectory(s.applicationsDir); err != nil {
		return application.Application{}, fmt.Errorf("ensure applications directory: %w", err)
	}
	destinationDirectory := filepath.Join(s.applicationsDir, folderName)
	if _, err := os.Lstat(destinationDirectory); err == nil {
		return application.Application{}, application.ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return application.Application{}, fmt.Errorf("inspect destination application directory: %w", err)
	}
	if err := os.Mkdir(destinationDirectory, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			return application.Application{}, application.ErrAlreadyExists
		}
		return application.Application{}, fmt.Errorf("create destination application directory: %w", err)
	}

	deleter, _ := s.serviceRepository.(applicationServiceDeletionRepository)
	created := false
	var createdServices []application.Service
	var createdDomains []string
	destination := application.Application{}
	defer func() {
		if created {
			return
		}
		compensationCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for index := len(createdServices) - 1; index >= 0; index-- {
			if deleter != nil {
				_ = deleter.DeleteService(compensationCtx, destination.ID, createdServices[index].Name)
			}
		}
		// Production deletes cascade domains with the application row; the
		// explicit pass keeps test doubles without cascades clean too.
		if s.domainRepository != nil {
			for _, name := range createdDomains {
				_ = s.domainRepository.DeleteDomain(compensationCtx, destination.ID, name)
			}
		}
		if destination.ID != 0 {
			if applicationDeleter, ok := s.repository.(applicationDeletionRepository); ok {
				_ = applicationDeleter.DeleteApplication(compensationCtx, destination.ID)
			}
		}
		_ = os.RemoveAll(destinationDirectory)
	}()

	sourceComposeSnapshot, err := snapshotManagedFile(sourceComposePath)
	if err != nil {
		return application.Application{}, fmt.Errorf("read source application Compose file: %w", err)
	}
	if !sourceComposeSnapshot.exists {
		return application.Application{}, errors.New("source application Compose file does not exist")
	}

	destination, err = s.repository.Create(ctx, application.Application{
		Name:       name,
		FolderName: folderName,
		CreatedAt:  time.Now().UTC(),
	})
	if err != nil {
		return application.Application{}, err
	}

	composeContents := rewriteClonedComposeIdentifiers(string(sourceComposeSnapshot.contents), source.ID, destination.ID)
	composeMode := sourceComposeSnapshot.mode
	if composeMode == 0 {
		composeMode = 0o644
	}
	if err := writeManagedFile(filepath.Join(destinationDirectory, preferredComposeFileName), composeContents, composeMode); err != nil {
		return application.Application{}, fmt.Errorf("write cloned application Compose file: %w", err)
	}

	if err := s.copyClonedEnvironmentFiles(sourceDirectory, destinationDirectory); err != nil {
		return application.Application{}, err
	}

	if input.CopyExtraFiles {
		if err := copyClonedExtraFiles(sourceDirectory, destinationDirectory); err != nil {
			return application.Application{}, err
		}
	}

	sourceServices, err := s.detailsRepository.ListServices(ctx, sourceID)
	if err != nil {
		return application.Application{}, fmt.Errorf("list source application services: %w", err)
	}
	if len(sourceServices) > 0 {
		metadata := make([]application.Service, 0, len(sourceServices))
		for _, service := range sourceServices {
			metadata = append(metadata, application.Service{
				ApplicationID:      destination.ID,
				Name:               service.Name,
				Type:               service.Type,
				ImageName:          service.ImageName,
				PostgresVersion:    service.PostgresVersion,
				DatabaseName:       service.DatabaseName,
				DatabaseUser:       service.DatabaseUser,
				RedisVersion:       service.RedisVersion,
				RedisPort:          service.RedisPort,
				RedisPersistToDisk: service.RedisPersistToDisk,
				CreatedAt:          time.Now().UTC(),
			})
		}
		createdServices, err = s.createClonedServices(ctx, metadata)
		if err != nil {
			return application.Application{}, err
		}
	}

	if s.domainRepository != nil {
		sourceDomains, err := s.domainRepository.ListDomains(ctx, sourceID)
		if err != nil {
			return application.Application{}, fmt.Errorf("list source application domains: %w", err)
		}
		for _, domain := range sourceDomains {
			if _, err := s.domainRepository.CreateDomain(ctx, application.Domain{
				ApplicationID: destination.ID,
				Name:          domain.Name,
			}); err != nil {
				return application.Application{}, fmt.Errorf("copy application domain: %w", err)
			}
			createdDomains = append(createdDomains, domain.Name)
		}
	}

	if err := s.validateStagedCompose(ctx, destinationDirectory); err != nil {
		return application.Application{}, err
	}

	created = true
	return destination, nil
}

// rewriteClonedComposeIdentifiers re-scopes managed identifiers from the
// source application to the destination. Container names and explicit volume
// names embed the application ID (redbolt-<id>-<service>); the Compose
// project name itself is derived from the folder path and needs no rewrite.
func rewriteClonedComposeIdentifiers(contents string, sourceID, destinationID int64) string {
	source := managedContainerNamePrefix + strconv.FormatInt(sourceID, 10) + "-"
	destination := managedContainerNamePrefix + strconv.FormatInt(destinationID, 10) + "-"
	if source == destination || !strings.Contains(contents, source) {
		return contents
	}
	return strings.ReplaceAll(contents, source, destination)
}

// copyClonedEnvironmentFiles copies the project-wide and service-scoped
// environment files verbatim, preserving comments and formatting. Missing
// scoped pairs are skipped; a half-present pair fails closed like the
// database service paths so ambiguous credentials cannot be cloned.
func (s *Applications) copyClonedEnvironmentFiles(sourceDirectory, destinationDirectory string) error {
	pairs := [][2]string{{varsEnvFile, secretsEnvFile}}
	// Scoped files are discovered from the source directory so clones work
	// even when the service metadata is momentarily ahead or behind.
	entries, err := os.ReadDir(sourceDirectory)
	if err != nil {
		return fmt.Errorf("list source application directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".vars.env") && name != varsEnvFile {
			secretName := strings.TrimSuffix(name, ".vars.env") + ".secrets.env"
			pairs = append(pairs, [2]string{name, secretName})
		}
	}
	for _, pair := range pairs {
		varsSnapshot, err := snapshotManagedFile(filepath.Join(sourceDirectory, pair[0]))
		if err != nil {
			return fmt.Errorf("read source %s file: %w", pair[0], err)
		}
		secretsSnapshot, err := snapshotManagedFile(filepath.Join(sourceDirectory, pair[1]))
		if err != nil {
			return fmt.Errorf("read source %s file: %w", pair[1], err)
		}
		if pair[0] == varsEnvFile {
			if !varsSnapshot.exists || !secretsSnapshot.exists {
				return application.ErrEnvironmentFileNotFound
			}
		} else {
			if !varsSnapshot.exists && !secretsSnapshot.exists {
				continue
			}
			if varsSnapshot.exists != secretsSnapshot.exists {
				return fmt.Errorf("%w: service file %q has only one scoped environment file", application.ErrDatabaseCredentialsAmbiguous, strings.TrimSuffix(pair[0], ".vars.env"))
			}
		}
		varsMode := varsSnapshot.mode
		if varsMode == 0 {
			varsMode = envFileMode
		}
		secretsMode := secretsSnapshot.mode
		if secretsMode == 0 {
			secretsMode = envFileMode
		}
		if err := writeManagedFile(filepath.Join(destinationDirectory, pair[0]), string(varsSnapshot.contents), varsMode); err != nil {
			return fmt.Errorf("write cloned %s file: %w", pair[0], err)
		}
		if err := writeManagedFile(filepath.Join(destinationDirectory, pair[1]), string(secretsSnapshot.contents), secretsMode); err != nil {
			return fmt.Errorf("write cloned %s file: %w", pair[1], err)
		}
	}
	return nil
}

// createClonedServices persists copied service metadata, using the batch
// path when the repository supports it so a clone cannot be partially
// registered.
func (s *Applications) createClonedServices(ctx context.Context, metadata []application.Service) ([]application.Service, error) {
	if batch, ok := s.serviceRepository.(applicationServiceBatchRepository); ok {
		created, err := batch.CreateServices(ctx, metadata)
		if err != nil {
			return nil, fmt.Errorf("persist cloned service metadata: %w", err)
		}
		return created, nil
	}
	deleter, _ := s.serviceRepository.(applicationServiceDeletionRepository)
	created := make([]application.Service, 0, len(metadata))
	for _, item := range metadata {
		createdService, err := s.serviceRepository.CreateService(ctx, item)
		if err != nil {
			compensationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for index := len(created) - 1; index >= 0; index-- {
				if deleter != nil {
					_ = deleter.DeleteService(compensationCtx, item.ApplicationID, created[index].Name)
				}
			}
			cancel()
			return nil, fmt.Errorf("persist cloned service metadata: %w", err)
		}
		created = append(created, createdService)
	}
	return created, nil
}

// copyClonedExtraFiles copies non-managed regular files below the source
// project directory verbatim. Symlinks are refused, absolute paths and
// escapes are impossible by construction (relative walk), and manager-owned
// files already copied are skipped.
func copyClonedExtraFiles(sourceDirectory, destinationDirectory string) error {
	var total int64
	return filepath.WalkDir(sourceDirectory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceDirectory, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if info, err := os.Lstat(path); err != nil {
			return err
		} else if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to clone symlink %q: resolve it manually", relative)
		}
		if entry.IsDir() {
			destination := filepath.Join(destinationDirectory, relative)
			if err := os.MkdirAll(destination, 0o750); err != nil {
				return fmt.Errorf("create cloned directory %q: %w", relative, err)
			}
			return nil
		}
		base := filepath.Base(relative)
		if filepath.Dir(relative) == "." {
			// Project-root managed files were already copied (or rewritten
			// for compose.yml) by the earlier steps.
			if _, managed := managerOwnedFileBaseNames[base]; managed {
				return nil
			}
			if strings.HasSuffix(base, ".vars.env") || strings.HasSuffix(base, ".secrets.env") {
				return nil
			}
			if base == "compose.yml" || base == "compose.yaml" {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to clone special file %q", relative)
		}
		if info.Size() > cloneExtraFileMaxPerFile {
			return fmt.Errorf("extra file %q exceeds the %d MiB clone limit: copy it manually", relative, cloneExtraFileMaxPerFile>>20)
		}
		total += info.Size()
		if total > cloneExtraFileMaxBytes {
			return fmt.Errorf("extra files exceed the %d MiB clone budget: copy remaining files manually", cloneExtraFileMaxBytes>>20)
		}
		destination := filepath.Join(destinationDirectory, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return fmt.Errorf("create cloned directory for %q: %w", relative, err)
		}
		return copyClonedExtraFile(path, destination, relative, info)
	})
}

// copyClonedExtraFile stages one extra file through a temp file and renames
// it into place so a crash cannot leave a truncated file. The size probe
// refuses files that grew past the limit while cloning.
func copyClonedExtraFile(sourcePath, destinationPath, relative string, info os.FileInfo) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	staged, err := os.CreateTemp(filepath.Dir(destinationPath), ".redlaunch-clone-*")
	if err != nil {
		return err
	}
	stagedName := staged.Name()
	removeStaged := true
	defer func() {
		if removeStaged {
			_ = os.Remove(stagedName)
		}
	}()
	if _, err := io.CopyN(staged, source, info.Size()+1); err != io.EOF {
		_ = staged.Close()
		if err == nil {
			return fmt.Errorf("extra file %q changed while cloning", relative)
		}
		return fmt.Errorf("copy extra file %q: %w", relative, err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("copy extra file %q: %w", relative, err)
	}
	if err := os.Chmod(stagedName, info.Mode().Perm()); err != nil {
		return fmt.Errorf("copy extra file %q: %w", relative, err)
	}
	if err := os.Rename(stagedName, destinationPath); err != nil {
		return fmt.Errorf("copy extra file %q: %w", relative, err)
	}
	removeStaged = false
	return nil
}
