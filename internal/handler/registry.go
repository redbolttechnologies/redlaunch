package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

func (h *Handler) registryPage(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		h.writeSetupPage(w, r, http.StatusOK, setupPageData{})
		return
	}
	if h.registryImages == nil {
		h.logger.Error("list registry images without an image service")
		h.writeRegistryPage(w, r, http.StatusInternalServerError, registryPageData{
			Error: "The image list could not be read right now.",
		})
		return
	}
	data, err := h.loadRegistryPageData(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.logger.Error("list registry images", "error", err)
		h.writeRegistryPage(w, r, http.StatusOK, registryPageData{
			Error: "The image list could not be read right now.",
		})
		return
	}
	data.Notice = registryNotice(r.URL.Query().Get("notice"))
	h.writeRegistryPage(w, r, http.StatusOK, data)
}

func registryNotice(value string) string {
	switch strings.TrimSpace(value) {
	case "retention-saved":
		return "Retention settings saved."
	case "retention-reset":
		return "Repository retention reset to the default."
	default:
		return ""
	}
}

func (h *Handler) loadRegistryPageData(ctx context.Context) (registryPageData, error) {
	images, err := h.registryImages.ListRegistryImages(ctx)
	if err != nil {
		return registryPageData{}, err
	}
	data := registryPageData{
		Images:      images,
		DefaultKeep: application.DefaultRegistryRetentionKeep,
		Policies:    make(map[string]int),
	}
	if retention, ok := h.registryImages.(registryRetentionSettingsService); ok && retention != nil {
		keep, keepErr := retention.GetRetentionKeep(ctx)
		if keepErr != nil {
			h.logger.Error("read registry retention keep", "error", keepErr)
			data.RetentionError = "Retention settings could not be read right now."
		} else {
			data.DefaultKeep = keep
			policies, policyErr := retention.ListRetentionPolicies(ctx)
			if policyErr != nil {
				h.logger.Error("read registry retention policies", "error", policyErr)
				data.RetentionError = "Retention settings could not be read right now."
			} else {
				for _, policy := range policies {
					if _, validateErr := application.ValidateRegistryKeepCount(policy.KeepCount); validateErr != nil {
						data.RetentionError = "Retention settings contain an invalid repository policy."
						break
					}
					data.Policies[policy.Repository] = policy.KeepCount
				}
				data.RetentionAvailable = data.RetentionError == ""
			}
		}
	}
	protected := map[string]bool{}
	if h.applicationManager != nil && h.applicationDetails != nil {
		protected, err = h.deployedRegistryTags(ctx)
		if err != nil {
			h.logger.Error("list deployed registry references", "error", err)
			protected = map[string]bool{}
		}
	}
	data.Groups = groupRegistryImages(data.Images, data.DefaultKeep, data.Policies, protected)
	for _, group := range data.Groups {
		data.PurgeVersionCount += len(group.PurgeVersions)
		if len(group.PurgeVersions) > 0 {
			data.PurgeRepositoryCount++
		}
	}
	_, data.PurgeAvailable = h.applicationManager.(registryMaintenanceRunner)
	data.PurgeAvailable = data.PurgeAvailable && data.RetentionAvailable
	_, data.GarbageCollectionAvailable = h.applicationManager.(registryMaintenanceRunner)
	data.MaintenanceRunning = h.registryMaintenanceRunning()
	if maintenance, ok := h.registryImages.(registryMaintenanceStateService); ok && maintenance != nil {
		if maintenanceErr := maintenance.MaintenanceError(); maintenanceErr != nil {
			h.logger.Error("registry maintenance is blocked", "error", maintenanceErr)
			data.MaintenanceError = "Registry maintenance is blocked because a previous maintenance window could not be recovered. Check the server log."
		}
	}
	if data.MaintenanceRunning {
		data.PurgeAvailable = false
		data.GarbageCollectionAvailable = false
	}
	if data.MaintenanceError != "" {
		data.PurgeAvailable = false
		data.GarbageCollectionAvailable = false
	}
	return data, nil
}

func groupRegistryImages(images []application.RegistryImage, defaultKeep int, policies map[string]int, protected map[string]bool) []registryRepositoryGroup {
	repositories := make([]string, 0)
	imagesByRepo := make(map[string][]application.RegistryImage)
	for _, image := range images {
		if _, ok := imagesByRepo[image.Repository]; !ok {
			repositories = append(repositories, image.Repository)
		}
		imagesByRepo[image.Repository] = append(imagesByRepo[image.Repository], image)
	}
	sort.Strings(repositories)
	groups := make([]registryRepositoryGroup, 0, len(repositories))
	for _, repository := range repositories {
		repositoryImages := imagesByRepo[repository]
		sortRegistryImagesNewestFirst(repositoryImages)
		effectiveKeep := application.EffectiveRegistryKeep(defaultKeep, policies, repository)
		overrideKeep, hasOverride := policies[repository]
		protectedTags := make(map[string]bool)
		for _, image := range repositoryImages {
			if protected != nil && protected[application.RegistryImageKey(image.Repository, image.Tag)] {
				protectedTags[image.Tag] = true
			}
		}
		_, purge, err := application.PreviewRegistryRetention(repositoryImages, repository, effectiveKeep, protected)
		if err != nil {
			purge = nil
		}
		groups = append(groups, registryRepositoryGroup{
			Repository:     repository,
			Images:         repositoryImages,
			ProtectedTags:  protectedTags,
			LatestPushedAt: latestRegistryPushedAt(repositoryImages),
			EffectiveKeep:  effectiveKeep,
			HasOverride:    hasOverride,
			OverrideKeep:   overrideKeep,
			PurgeVersions:  purge,
		})
	}
	return groups
}

func latestRegistryPushedAt(images []application.RegistryImage) time.Time {
	var latest time.Time
	for _, image := range images {
		if image.PushedAt.IsZero() {
			continue
		}
		if latest.IsZero() || image.PushedAt.After(latest) {
			latest = image.PushedAt
		}
	}
	return latest
}

func sortRegistryImagesNewestFirst(images []application.RegistryImage) {
	sort.Slice(images, func(left, right int) bool {
		leftZero := images[left].PushedAt.IsZero()
		rightZero := images[right].PushedAt.IsZero()
		if leftZero != rightZero {
			return rightZero
		}
		if !images[left].PushedAt.Equal(images[right].PushedAt) {
			return images[left].PushedAt.After(images[right].PushedAt)
		}
		if images[left].Tag != images[right].Tag {
			return images[left].Tag < images[right].Tag
		}
		return images[left].Digest < images[right].Digest
	})
}

// deployedRegistryTags returns the set of currently configured registry tags
// so page previews never suggest a deployed tag for removal. Runtime digests
// are additionally protected during execution.
func (h *Handler) deployedRegistryTags(ctx context.Context) (map[string]bool, error) {
	protected := make(map[string]bool)
	if h.applicationManager == nil || h.applicationDetails == nil {
		return protected, nil
	}
	applications, err := h.applicationManager.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range applications {
		services, err := h.applicationDetails.ListServices(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		for _, service := range services {
			repository, tag, ok := application.ParseDeployedRegistryReference(service.ImageName)
			if !ok {
				continue
			}
			protected[application.RegistryImageKey(repository, tag)] = true
		}
	}
	return protected, nil
}

func (h *Handler) setRegistryDefaultRetention(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		h.renderRegistryRetentionError(w, r, "The retention request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	keep, err := parseRegistryKeep(r.Form.Get("keep_count"))
	if err != nil {
		h.renderRegistryRetentionError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
		return
	}
	retention, ok := h.registryImages.(registryRetentionSettingsService)
	if !ok || retention == nil {
		h.renderRegistryRetentionError(w, r, "Retention settings are not available right now.", http.StatusInternalServerError)
		return
	}
	if err := retention.SetRetentionKeep(r.Context(), keep); err != nil {
		if errors.Is(err, application.ErrRegistryKeepInvalid) {
			h.renderRegistryRetentionError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
			return
		}
		h.logger.Error("save registry retention keep", "error", err)
		h.renderRegistryRetentionError(w, r, "Retention settings could not be saved right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/registry?notice=retention-saved", http.StatusSeeOther)
}

func (h *Handler) setRegistryRepositoryRetention(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		h.renderRegistryRetentionError(w, r, "The retention request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	repository, err := application.ValidateRegistryRepository(r.Form.Get("repository"))
	if err != nil {
		h.renderRegistryRetentionError(w, r, "The repository name is invalid.", http.StatusBadRequest)
		return
	}
	retention, ok := h.registryImages.(registryRetentionSettingsService)
	if !ok || retention == nil {
		h.renderRegistryRetentionError(w, r, "Retention settings are not available right now.", http.StatusInternalServerError)
		return
	}
	if r.Form.Get("reset") == "1" {
		if err := retention.DeleteRepositoryRetention(r.Context(), repository); err != nil {
			h.logger.Error("reset registry repository retention", "repository", repository, "error", err)
			h.renderRegistryRetentionError(w, r, "The repository retention setting could not be reset right now.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/registry?notice=retention-reset", http.StatusSeeOther)
		return
	}
	keep, err := parseRegistryKeep(r.Form.Get("keep_count"))
	if err != nil {
		h.renderRegistryRetentionError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
		return
	}
	if err := retention.SetRepositoryRetention(r.Context(), repository, keep); err != nil {
		if errors.Is(err, application.ErrRegistryKeepInvalid) || errors.Is(err, application.ErrRegistryRepositoryInvalid) {
			h.renderRegistryRetentionError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
			return
		}
		h.logger.Error("save registry repository retention", "repository", repository, "error", err)
		h.renderRegistryRetentionError(w, r, "The repository retention setting could not be saved right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/registry?notice=retention-saved", http.StatusSeeOther)
}

func (h *Handler) purgeRegistryImages(w http.ResponseWriter, r *http.Request) {
	h.startRegistryMaintenanceAction(w, r, registryMaintenanceJobRetention, "purge-all")
}

func (h *Handler) garbageCollectRegistry(w http.ResponseWriter, r *http.Request) {
	h.startRegistryMaintenanceAction(w, r, registryMaintenanceJobGarbage, "garbage-collect")
}

func (h *Handler) startRegistryMaintenanceAction(w http.ResponseWriter, r *http.Request, operation, confirmation string) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The registry maintenance request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	if r.Form.Get("confirmation") != confirmation {
		h.renderRegistryError(w, r, "Confirm the registry maintenance action before continuing.", http.StatusBadRequest)
		return
	}
	if maintenance, ok := h.registryImages.(registryMaintenanceStateService); ok && maintenance != nil {
		if err := maintenance.MaintenanceError(); err != nil {
			h.renderRegistryError(w, r, "Registry maintenance is blocked until the previous maintenance window is recovered.", http.StatusConflict)
			return
		}
	}
	if _, ok := h.applicationManager.(registryMaintenanceRunner); !ok {
		h.renderRegistryError(w, r, "Registry maintenance is not available right now.", http.StatusInternalServerError)
		return
	}
	job, created, err := h.registryMaintenanceJobs.createUnique(operation)
	if err != nil {
		h.logger.Error("create registry maintenance job", "operation", operation, "error", err)
		h.renderRegistryError(w, r, "The registry maintenance operation could not be started.", http.StatusInternalServerError)
		return
	}
	if created {
		if err := h.startTrackedJobWithTimeout("registry-maintenance", registryMaintenanceJobTimeout, func(ctx context.Context) {
			h.startRegistryMaintenanceJob(ctx, job)
		}); err != nil {
			job.fail(err)
			h.logger.Error("admit registry maintenance job", "operation", operation, "error", err)
			h.renderRegistryError(w, r, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
			return
		}
	}
	http.Redirect(w, r, "/registry?registry_maintenance_job="+url.QueryEscape(job.id), http.StatusSeeOther)
}

func (h *Handler) registryMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	progress, ok := h.registryMaintenanceProgress(strings.TrimSpace(r.URL.Query().Get("id")))
	if !ok {
		http.Error(w, "The registry maintenance job was not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "registry-maintenance-toast.html", pageData{
		RegistryMaintenanceToasts: []*registryMaintenanceProgressData{progress},
	}, http.StatusOK)
}

func parseRegistryKeep(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, application.ErrRegistryKeepInvalid
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, application.ErrRegistryKeepInvalid
	}
	return application.ValidateRegistryKeepCount(parsed)
}

func (h *Handler) requireRegistrySetup(w http.ResponseWriter, r *http.Request) bool {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return false
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	if h.registryImages == nil {
		h.logger.Error("registry maintenance without an image service")
		http.Error(w, "The registry service is not available.", http.StatusInternalServerError)
		return false
	}
	return true
}

func (h *Handler) renderRegistryError(w http.ResponseWriter, r *http.Request, message string, status int) {
	data, err := h.loadRegistryPageData(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.writeRegistryPage(w, r, status, registryPageData{Error: message})
		return
	}
	data.Error = message
	h.writeRegistryPage(w, r, status, data)
}

func (h *Handler) renderRegistryRetentionError(w http.ResponseWriter, r *http.Request, message string, status int) {
	data, err := h.loadRegistryPageData(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.writeRegistryPage(w, r, status, registryPageData{Error: message})
		return
	}
	data.RetentionError = message
	h.writeRegistryPage(w, r, status, data)
}

func (h *Handler) writeRegistryPage(w http.ResponseWriter, r *http.Request, status int, data registryPageData) {
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = h.setCSRFCookie(w, r)
	page := h.shellPageData(r)
	page.ActivePage = "registry"
	page.RegistryPage = &data
	page.RegistryMaintenanceToasts = h.registryMaintenanceToastsForPage(strings.TrimSpace(r.URL.Query().Get("registry_maintenance_job")))
	h.writeTemplateStatus(w, "registry.html", page, status)
}
