package handler

import (
	"context"
	"net/http"
	"strconv"

	"redlaunch/internal/application"
)

type serviceVolumeDeletionService interface {
	ListServiceDeletionVolumes(context.Context, int64, string) ([]application.ServiceDeletionVolume, error)
	DeleteServiceWithVolumes(context.Context, int64, string, []string, func(string, string)) error
}

type serviceDeleteDialogData struct {
	ApplicationID int64
	Service       application.Service
	CSRFToken     string
}

type serviceDeleteVolumesData struct {
	Volumes     []application.ServiceDeletionVolume
	Unavailable bool
}

func serviceDeleteDialog(id int64, service application.Service, csrf string) serviceDeleteDialogData {
	return serviceDeleteDialogData{ApplicationID: id, Service: service, CSRFToken: csrf}
}

func (h *Handler) serviceDeletionVolumes(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	name, err := application.ValidateServiceName(r.PathValue("service"))
	if err != nil || name != r.PathValue("service") {
		http.NotFound(w, r)
		return
	}
	manager, ok := h.serviceDeletion.(serviceVolumeDeletionService)
	data := serviceDeleteVolumesData{}
	if ok {
		data.Volumes, err = manager.ListServiceDeletionVolumes(r.Context(), id, name)
		if err != nil {
			data.Unavailable = true
		}
	} else {
		data.Unavailable = true
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeTemplateStatus(w, "service-delete-volumes.html", pageData{ServiceDeleteVolumes: &data}, http.StatusOK)
}
