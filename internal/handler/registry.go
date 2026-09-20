package handler

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"redlaunch/internal/application"
)

type registryRetentionService interface {
	GetDefaultKeep(context.Context) (int, error)
	SetDefaultKeep(context.Context, int) error
	ListRetentionPolicies(context.Context) ([]application.RegistryRetentionPolicy, error)
	SetRepositoryKeep(context.Context, string, int) error
	DeleteRepositoryKeep(context.Context, string) error
	PlanRepositoryPurge(context.Context, string, map[string]bool) ([]application.RegistryImage, []application.RegistryImage, int, error)
	PurgeRepository(context.Context, string, map[string]bool) ([]application.RegistryImage, error)
}

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

	images, err := h.registryImages.ListRegistryImages(r.Context())
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
	data := registryPageData{Images: images}
	if err := h.populateRegistryRetention(r.Context(), &data); err != nil {
		h.logger.Error("load registry retention", "error", err)
		data.Error = "Retention settings could not be read right now."
	}
	h.writeRegistryPage(w, r, http.StatusOK, data)
}

func (h *Handler) populateRegistryRetention(ctx context.Context, data *registryPageData) error {
	retention, ok := h.registryImages.(registryRetentionService)
	if !ok {
		data.DefaultKeep = application.DefaultRegistryKeepCount
		data.Groups = groupRegistryImages(data.Images, data.DefaultKeep, nil, nil)
		return nil
	}
	defaultKeep, err := retention.GetDefaultKeep(ctx)
	if err != nil {
		return err
	}
	data.DefaultKeep = defaultKeep
	data.RetentionAvailable = true
	policies, err := retention.ListRetentionPolicies(ctx)
	if err != nil {
		return err
	}
	policyByRepo := make(map[string]int, len(policies))
	for _, policy := range policies {
		policyByRepo[policy.Repository] = policy.KeepCount
	}
	protected, err := h.deployedRegistryTags(ctx)
	if err != nil {
		h.logger.Error("list deployed registry references", "error", err)
		protected = map[string]bool{}
	}
	groups, err := planRegistryGroups(data.Images, defaultKeep, policyByRepo, protected)
	if err != nil {
		return err
	}
	data.Groups = groups
	return nil
}

func planRegistryGroups(images []application.RegistryImage, defaultKeep int, policyByRepo map[string]int, protected map[string]bool) ([]registryRepositoryGroup, error) {
	repositories := make([]string, 0)
	imagesByRepo := make(map[string][]application.RegistryImage)
	for _, image := range images {
		if _, ok := imagesByRepo[image.Repository]; !ok {
			repositories = append(repositories, image.Repository)
		}
		imagesByRepo[image.Repository] = append(imagesByRepo[image.Repository], image)
	}
	for repository := range policyByRepo {
		if _, ok := imagesByRepo[repository]; !ok {
			repositories = append(repositories, repository)
			imagesByRepo[repository] = nil
		}
	}
	sort.Strings(repositories)
	groups := make([]registryRepositoryGroup, 0, len(repositories))
	for _, repository := range repositories {
		repositoryImages := imagesByRepo[repository]
		sortRegistryImagesNewestFirst(repositoryImages)
		keep, hasOverride := policyByRepo[repository]
		overrideKeep := 0
		if hasOverride {
			overrideKeep = keep
		} else {
			keep = defaultKeep
		}
		kept, purge, err := application.PlanRegistryPurge(repositoryImages, keep, protected)
		if err != nil {
			return nil, err
		}
		_ = kept
		protectedTags := make(map[string]bool)
		for _, image := range repositoryImages {
			if protected != nil && protected[application.RegistryImageKey(image.Repository, image.Tag)] {
				protectedTags[image.Tag] = true
			}
		}
		groups = append(groups, registryRepositoryGroup{
			Repository:      repository,
			Images:          repositoryImages,
			EffectiveKeep:   keep,
			HasOverride:     hasOverride,
			OverrideKeep:    overrideKeep,
			PurgeCandidates: purge,
			ProtectedTags:   protectedTags,
			LatestPushedAt:  latestRegistryPushedAt(repositoryImages),
		})
	}
	return groups, nil
}

// groupRegistryImages builds display-only repository groups when retention
// settings are unavailable (for example a read-only image service in tests).
// It sorts tags newest first so the summary row can show the latest push.
func groupRegistryImages(images []application.RegistryImage, defaultKeep int, policyByRepo map[string]int, protected map[string]bool) []registryRepositoryGroup {
	groups, err := planRegistryGroups(images, defaultKeep, policyByRepo, protected)
	if err != nil {
		repositories := make([]string, 0)
		imagesByRepo := make(map[string][]application.RegistryImage)
		for _, image := range images {
			if _, ok := imagesByRepo[image.Repository]; !ok {
				repositories = append(repositories, image.Repository)
			}
			imagesByRepo[image.Repository] = append(imagesByRepo[image.Repository], image)
		}
		sort.Strings(repositories)
		groups = make([]registryRepositoryGroup, 0, len(repositories))
		for _, repository := range repositories {
			repositoryImages := imagesByRepo[repository]
			sortRegistryImagesNewestFirst(repositoryImages)
			groups = append(groups, registryRepositoryGroup{
				Repository:     repository,
				Images:         repositoryImages,
				EffectiveKeep:  defaultKeep,
				LatestPushedAt: latestRegistryPushedAt(repositoryImages),
			})
		}
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

// deployedRegistryTags returns the set of currently deployed registry tags so
// manual purges never delete a running reference. The key is
// application.RegistryImageKey.
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

func (h *Handler) setRegistryDefaultKeep(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The retention request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	keep, err := parseRegistryKeep(r.Form.Get("keep_count"))
	if err != nil {
		h.renderRegistryError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
		return
	}
	retention, ok := h.registryImages.(registryRetentionService)
	if !ok || retention == nil {
		h.renderRegistryError(w, r, "Retention settings are not available right now.", http.StatusInternalServerError)
		return
	}
	if err := retention.SetDefaultKeep(r.Context(), keep); err != nil {
		if errors.Is(err, application.ErrRegistryKeepInvalid) {
			h.renderRegistryError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
			return
		}
		h.logger.Error("save registry default keep", "error", err)
		h.renderRegistryError(w, r, "The default could not be saved right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/registry", http.StatusSeeOther)
}

func (h *Handler) setRegistryRepositoryKeep(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The retention request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	repository := strings.TrimSpace(r.Form.Get("repository"))
	if _, err := application.ValidateRegistryRepository(repository); err != nil {
		h.renderRegistryError(w, r, "The repository name is invalid.", http.StatusBadRequest)
		return
	}
	retention, ok := h.registryImages.(registryRetentionService)
	if !ok || retention == nil {
		h.renderRegistryError(w, r, "Retention settings are not available right now.", http.StatusInternalServerError)
		return
	}
	if r.Form.Get("reset") == "1" {
		if err := retention.DeleteRepositoryKeep(r.Context(), repository); err != nil {
			h.logger.Error("reset registry repository keep", "error", err)
			h.renderRegistryError(w, r, "The repository setting could not be reset right now.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/registry", http.StatusSeeOther)
		return
	}
	keep, err := parseRegistryKeep(r.Form.Get("keep_count"))
	if err != nil {
		h.renderRegistryError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
		return
	}
	if err := retention.SetRepositoryKeep(r.Context(), repository, keep); err != nil {
		if errors.Is(err, application.ErrRegistryKeepInvalid) || errors.Is(err, application.ErrRegistryRepositoryInvalid) {
			h.renderRegistryError(w, r, "Enter a keep count from 0 to 100. Use 0 for unlimited.", http.StatusBadRequest)
			return
		}
		h.logger.Error("save registry repository keep", "error", err)
		h.renderRegistryError(w, r, "The repository setting could not be saved right now.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/registry", http.StatusSeeOther)
}

func (h *Handler) purgeRegistryRepository(w http.ResponseWriter, r *http.Request) {
	if !h.requireRegistrySetup(w, r) {
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The purge request was invalid.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This registry page expired. Submit the refreshed page to continue.", http.StatusForbidden)
		return
	}
	repository := strings.TrimSpace(r.Form.Get("repository"))
	if _, err := application.ValidateRegistryRepository(repository); err != nil {
		h.renderRegistryError(w, r, "The repository name is invalid.", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(r.Form.Get("confirm")) != "purge" {
		h.renderRegistryError(w, r, "Confirm the purge to delete the older images.", http.StatusBadRequest)
		return
	}
	retention, ok := h.registryImages.(registryRetentionService)
	if !ok || retention == nil {
		h.renderRegistryError(w, r, "Registry purge is not available right now.", http.StatusInternalServerError)
		return
	}
	protected, err := h.deployedRegistryTags(r.Context())
	if err != nil {
		h.logger.Error("list deployed registry references", "error", err)
		h.renderRegistryError(w, r, "Deployed images could not be verified, so nothing was deleted.", http.StatusInternalServerError)
		return
	}
	purged, err := retention.PurgeRepository(r.Context(), repository, protected)
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.renderRegistryError(w, r, "The local registry is not available. Install it during setup.", http.StatusBadRequest)
			return
		}
		if errors.Is(err, application.ErrRegistryRepositoryInvalid) || errors.Is(err, application.ErrRegistryKeepInvalid) {
			h.renderRegistryError(w, r, "The purge request was invalid.", http.StatusBadRequest)
			return
		}
		h.logger.Error("purge registry repository", "repository", repository, "error", err)
		h.renderRegistryError(w, r, "The purge could not be completed. Nothing may have been deleted; refresh to verify.", http.StatusInternalServerError)
		return
	}
	h.renderRegistryNotice(w, r, purgeMessage(repository, purged))
}

func purgeMessage(repository string, purged []application.RegistryImage) string {
	if len(purged) == 0 {
		return "No images needed purging for " + repository + ". The newest images and deployed tags were kept."
	}
	if len(purged) == 1 {
		return "Purged 1 image from " + repository + ". Run registry garbage-collect on the server to reclaim blob space."
	}
	return "Purged " + strconv.Itoa(len(purged)) + " images from " + repository + ". Run registry garbage-collect on the server to reclaim blob space."
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
		h.logger.Error("registry retention without an image service")
		http.Error(w, "The registry service is not available.", http.StatusInternalServerError)
		return false
	}
	return true
}

func (h *Handler) renderRegistryError(w http.ResponseWriter, r *http.Request, message string, status int) {
	images, err := h.registryImages.ListRegistryImages(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.writeRegistryPage(w, r, status, registryPageData{Error: message})
		return
	}
	data := registryPageData{Images: images, Error: message}
	if err := h.populateRegistryRetention(r.Context(), &data); err != nil {
		h.logger.Error("load registry retention after error", "error", err)
	}
	data.Error = message
	h.writeRegistryPage(w, r, status, data)
}

func (h *Handler) renderRegistryNotice(w http.ResponseWriter, r *http.Request, notice string) {
	images, err := h.registryImages.ListRegistryImages(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Error: "The image list could not be read right now."})
		return
	}
	data := registryPageData{Images: images, Notice: notice}
	if err := h.populateRegistryRetention(r.Context(), &data); err != nil {
		h.logger.Error("load registry retention after purge", "error", err)
		data.Error = "Retention settings could not be read right now."
		data.Notice = ""
		h.writeRegistryPage(w, r, http.StatusOK, data)
		return
	}
	data.Notice = notice
	h.writeRegistryPage(w, r, http.StatusOK, data)
}

func (h *Handler) writeRegistryPage(w http.ResponseWriter, r *http.Request, status int, data registryPageData) {
	w.Header().Set("Cache-Control", "no-store")
	data.CSRFToken = h.setCSRFCookie(w, r)
	page := h.shellPageData(r)
	page.ActivePage = "registry"
	page.RegistryPage = &data
	h.writeTemplateStatus(w, "registry.html", page, status)
}
