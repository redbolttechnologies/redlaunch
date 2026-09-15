package handler

import (
	"net/http"
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

	images, err := h.registryImages.ListRegistryImages(r.Context())
	if err != nil {
		h.logger.Error("list registry images", "error", err)
		h.writeRegistryPage(w, r, http.StatusOK, registryPageData{
			Error: "The image list could not be read right now.",
		})
		return
	}
	h.writeRegistryPage(w, r, http.StatusOK, registryPageData{Images: images})
}

func (h *Handler) writeRegistryPage(w http.ResponseWriter, r *http.Request, status int, data registryPageData) {
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ActivePage = "registry"
	page.RegistryPage = &data
	h.writeTemplateStatus(w, "registry.html", page, status)
}
