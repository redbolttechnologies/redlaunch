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

type registryPurgeService interface {
	PurgeRepository(context.Context, string, int, map[string]bool) ([]application.RegistryImage, error)
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
	h.populateRegistryGroups(r.Context(), &data)
	h.writeRegistryPage(w, r, http.StatusOK, data)
}

func (h *Handler) populateRegistryGroups(ctx context.Context, data *registryPageData) {
	protected, err := h.deployedRegistryTags(ctx)
	if err != nil {
		h.logger.Error("list deployed registry references", "error", err)
		protected = map[string]bool{}
	}
	data.Groups = groupRegistryImages(data.Images, protected)
}

func groupRegistryImages(images []application.RegistryImage, protected map[string]bool) []registryRepositoryGroup {
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
		protectedTags := make(map[string]bool)
		for _, image := range repositoryImages {
			if protected != nil && protected[application.RegistryImageKey(image.Repository, image.Tag)] {
				protectedTags[image.Tag] = true
			}
		}
		// Display-only purge preview using the default keep count so the
		// summary row can show the Keep badge and the expanded area can
		// preview which older tags a purge would remove. Deployed tags are
		// always kept and excluded from the preview.
		_, purge, err := application.PlanRegistryPurge(repositoryImages, application.DefaultRegistryKeepCount, protected)
		if err != nil {
			purge = nil
		}
		groups = append(groups, registryRepositoryGroup{
			Repository:      repository,
			Images:          repositoryImages,
			ProtectedTags:   protectedTags,
			LatestPushedAt:  latestRegistryPushedAt(repositoryImages),
			EffectiveKeep:   application.DefaultRegistryKeepCount,
			PurgeCandidates: purge,
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
	keep, err := parseRegistryKeep(r.Form.Get("keep_count"))
	if err != nil {
		h.renderRegistryPurgeDialogError(w, r, repository, "Enter a keep count from 0 to 100. Use 0 for unlimited.")
		return
	}
	purger, ok := h.registryImages.(registryPurgeService)
	if !ok || purger == nil {
		h.renderRegistryError(w, r, "Registry purge is not available right now.", http.StatusInternalServerError)
		return
	}
	protected, err := h.deployedRegistryTags(r.Context())
	if err != nil {
		h.logger.Error("list deployed registry references", "error", err)
		h.renderRegistryError(w, r, "Deployed images could not be verified, so nothing was deleted.", http.StatusInternalServerError)
		return
	}
	purged, err := purger.PurgeRepository(r.Context(), repository, keep, protected)
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
		h.logger.Error("registry purge without an image service")
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
	h.populateRegistryGroups(r.Context(), &data)
	data.Error = message
	h.writeRegistryPage(w, r, status, data)
}

func (h *Handler) renderRegistryPurgeDialogError(w http.ResponseWriter, r *http.Request, repository, message string) {
	images, err := h.registryImages.ListRegistryImages(r.Context())
	if err != nil {
		if errors.Is(err, application.ErrRegistryUnavailable) {
			h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Unavailable: true})
			return
		}
		h.writeRegistryPage(w, r, http.StatusBadRequest, registryPageData{Error: message})
		return
	}
	data := registryPageData{Images: images}
	h.populateRegistryGroups(r.Context(), &data)
	data.PurgeDialog = &registryPurgeDialogData{
		Open:       true,
		Repository: repository,
		Keep:       application.DefaultRegistryKeepCount,
		Error:      message,
	}
	h.writeRegistryPage(w, r, http.StatusBadRequest, data)
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
	h.populateRegistryGroups(r.Context(), &data)
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
