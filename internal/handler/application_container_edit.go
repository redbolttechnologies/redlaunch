package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

type applicationContainerConfigReader interface {
	GetApplicationServiceConfig(ctx context.Context, applicationID int64, serviceName string) (application.ApplicationServiceInput, error)
}

type applicationContainerUpdater interface {
	UpdateApplicationService(ctx context.Context, applicationID int64, serviceName string, input application.ApplicationServiceInput) (application.Service, error)
}

// context is only used through the interface method set; keep the import
// minimal by aliasing context via net/http request context.
func (h *Handler) applicationContainerConfigService() applicationContainerConfigReader {
	if reader, ok := h.applicationContainerManager.(applicationContainerConfigReader); ok && reader != nil {
		return reader
	}
	if reader, ok := h.applicationDetails.(applicationContainerConfigReader); ok && reader != nil {
		return reader
	}
	return nil
}

func (h *Handler) applicationContainerUpdateService() applicationContainerUpdater {
	if updater, ok := h.applicationContainerManager.(applicationContainerUpdater); ok && updater != nil {
		return updater
	}
	if updater, ok := h.applicationDetails.(applicationContainerUpdater); ok && updater != nil {
		return updater
	}
	return nil
}

func (h *Handler) editApplicationContainerPage(w http.ResponseWriter, r *http.Request) {
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

	applicationID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || applicationID < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	if _, err := application.ValidateServiceName(serviceName); err != nil {
		http.NotFound(w, r)
		return
	}

	item, err := h.applicationDetails.Get(r.Context(), applicationID)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for application container edit", "id", applicationID, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	services, err := h.applicationDetails.ListServices(r.Context(), item.ID)
	if err != nil {
		h.logger.Error("list application services for application container edit", "application_id", item.ID, "error", err)
		http.Error(w, "The application services could not be read.", http.StatusInternalServerError)
		return
	}
	var target *application.Service
	for index := range services {
		if services[index].Name == serviceName {
			target = &services[index]
			break
		}
	}
	if target == nil || target.Type != application.ServiceTypeApplication {
		http.NotFound(w, r)
		return
	}

	reader := h.applicationContainerConfigService()
	if reader == nil {
		h.logger.Error("edit application container without a config reader", "application_id", item.ID)
		http.Error(w, "The application container editor is not configured.", http.StatusInternalServerError)
		return
	}
	config, err := reader.GetApplicationServiceConfig(r.Context(), item.ID, serviceName)
	if errors.Is(err, application.ErrServiceNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("read application container config for edit", "application_id", item.ID, "service", serviceName, "error", err)
		http.Error(w, "The service configuration could not be read.", http.StatusInternalServerError)
		return
	}

	data := applicationContainerPageData{
		Application:       item,
		Services:          filterApplicationContainerEditServices(services, serviceName),
		IsEdit:            true,
		ServiceName:       serviceName,
		ImageName:         displayApplicationContainerImageName(config.ImageName),
		UseDockerRegistry: !isLocalRegistryImage(config.ImageName),
		Entrypoint:        config.Entrypoint,
		Healthcheck:       config.Healthcheck,
		DependsOn:         config.DependsOn,
		RestartPolicy:     config.RestartPolicy,
		PortMappings:      config.PortMappings,
		VolumeMappings:    config.VolumeMappings,
	}
	if data.RestartPolicy == "" {
		data.RestartPolicy = application.ApplicationRestartPolicyUnlessStopped
	}
	h.writeApplicationContainerPage(w, r, http.StatusOK, data)
}

func (h *Handler) updateApplicationContainer(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := h.setupManager.NeedsSetup()
	if err != nil {
		h.logger.Error("inspect setup state", "error", err)
		http.Error(w, "The setup state could not be read.", http.StatusInternalServerError)
		return
	}
	if needsSetup {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	applicationID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || applicationID < 1 {
		http.NotFound(w, r)
		return
	}
	serviceName := r.PathValue("service")
	if _, err := application.ValidateServiceName(serviceName); err != nil {
		http.NotFound(w, r)
		return
	}
	item, err := h.applicationDetails.Get(r.Context(), applicationID)
	if errors.Is(err, application.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("get application for application container update", "id", applicationID, "error", err)
		http.Error(w, "The application could not be read.", http.StatusInternalServerError)
		return
	}

	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "The application container request was invalid.", http.StatusBadRequest)
		return
	}
	data := applicationContainerPageData{
		Application:       item,
		IsEdit:            true,
		ServiceName:       serviceName,
		ImageName:         firstFormValue(r.Form, "image_name", "image"),
		UseDockerRegistry: r.Form.Get("use_docker_registry") == "on",
		Entrypoint:        firstFormValue(r.Form, "entrypoint", "entrypoint_command"),
		Healthcheck: application.ApplicationHealthcheck{
			Command:     firstFormValue(r.Form, "healthcheck_command", "healthcheck_test"),
			Interval:    firstFormValue(r.Form, "healthcheck_interval"),
			Timeout:     firstFormValue(r.Form, "healthcheck_timeout"),
			Retries:     firstFormValue(r.Form, "healthcheck_retries"),
			StartPeriod: firstFormValue(r.Form, "healthcheck_start_period"),
		},
		DependsOn:      applicationContainerDependenciesFromForm(r.Form),
		RestartPolicy:  firstFormValue(r.Form, "restart_policy", "restart"),
		PortMappings:   applicationContainerPortsFromForm(r.Form),
		VolumeMappings: applicationContainerVolumesFromForm(r.Form),
	}
	services, err := h.applicationDetails.ListServices(r.Context(), item.ID)
	if err != nil {
		h.logger.Error("list application services for application container update", "application_id", item.ID, "error", err)
		http.Error(w, "The application services could not be read.", http.StatusInternalServerError)
		return
	}
	data.Services = filterApplicationContainerEditServices(services, serviceName)
	var target *application.Service
	for index := range services {
		if services[index].Name == serviceName {
			target = &services[index]
			break
		}
	}
	if target == nil || target.Type != application.ServiceTypeApplication {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(data.ImageName) == "" {
		data.ImageName = defaultApplicationContainerImageName(data.ServiceName)
	}
	if strings.TrimSpace(data.RestartPolicy) == "" {
		data.RestartPolicy = application.ApplicationRestartPolicyUnlessStopped
	}
	if !h.validRequestCSRF(r) {
		data.Error = "This application container page expired. Submit the refreshed form to continue."
		h.writeApplicationContainerPage(w, r, http.StatusForbidden, data)
		return
	}

	input := application.ApplicationServiceInput{
		ServiceName:    serviceName,
		ImageName:      applicationContainerImageName(data.ImageName, data.UseDockerRegistry),
		Entrypoint:     data.Entrypoint,
		Healthcheck:    data.Healthcheck,
		DependsOn:      applicationContainerNonEmptyDependencies(data.DependsOn),
		RestartPolicy:  data.RestartPolicy,
		PortMappings:   applicationContainerNonEmptyPorts(data.PortMappings),
		VolumeMappings: applicationContainerNonEmptyVolumes(data.VolumeMappings),
	}
	if validator, ok := h.applicationContainerUpdateService().(applicationContainerInputValidator); ok {
		if err := validator.ValidateApplicationServiceInput(input); err != nil {
			data.Error = applicationContainerUserMessage(err)
			h.writeApplicationContainerPage(w, r, http.StatusBadRequest, data)
			return
		}
	} else if validator, ok := h.applicationContainerManager.(applicationContainerInputValidator); ok {
		if err := validator.ValidateApplicationServiceInput(input); err != nil {
			data.Error = applicationContainerUserMessage(err)
			h.writeApplicationContainerPage(w, r, http.StatusBadRequest, data)
			return
		}
	}

	updater := h.applicationContainerUpdateService()
	if updater == nil {
		h.logger.Error("update application container without an updater", "application_id", item.ID)
		data.Error = "The application container editor is not configured."
		h.writeApplicationContainerPage(w, r, http.StatusInternalServerError, data)
		return
	}
	if _, err := updater.UpdateApplicationService(r.Context(), item.ID, serviceName, input); err != nil {
		if errors.Is(err, application.ErrServiceNotFound) {
			http.NotFound(w, r)
			return
		}
		if applicationContainerUpdateUserError(err) {
			data.Error = applicationContainerUserMessage(err)
			h.writeApplicationContainerPage(w, r, http.StatusBadRequest, data)
			return
		}
		h.logger.Error("update application container", "application_id", item.ID, "service", serviceName, "error", applicationContainerErrorDetail(err))
		data.Error = applicationContainerUserMessage(err)
		h.writeApplicationContainerPage(w, r, http.StatusInternalServerError, data)
		return
	}
	http.Redirect(w, r, serviceDetailsPath(item.ID, serviceName), http.StatusSeeOther)
}

func displayApplicationContainerImageName(imageName string) string {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return ""
	}
	if prefix := application.LocalRegistryAddress + "/"; strings.HasPrefix(imageName, prefix) {
		return strings.TrimPrefix(imageName, prefix)
	}
	return imageName
}

func isLocalRegistryImage(imageName string) bool {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return true
	}
	return strings.HasPrefix(imageName, application.LocalRegistryAddress+"/")
}

func filterApplicationContainerEditServices(services []application.Service, serviceName string) []application.Service {
	filtered := make([]application.Service, 0, len(services))
	for _, service := range services {
		if service.Name == serviceName {
			continue
		}
		filtered = append(filtered, service)
	}
	return filtered
}

func applicationContainerUpdateUserError(err error) bool {
	return errors.Is(err, application.ErrServiceNameRequired) ||
		errors.Is(err, application.ErrServiceNameTooLong) ||
		errors.Is(err, application.ErrServiceNameInvalid) ||
		errors.Is(err, application.ErrImageNameRequired) ||
		errors.Is(err, application.ErrImageNameTooLong) ||
		errors.Is(err, application.ErrImageNameInvalid) ||
		errors.Is(err, application.ErrApplicationEntrypointTooLong) ||
		errors.Is(err, application.ErrApplicationEntrypointInvalid) ||
		errors.Is(err, application.ErrApplicationHealthcheckCommandTooLong) ||
		errors.Is(err, application.ErrApplicationHealthcheckCommandInvalid) ||
		errors.Is(err, application.ErrApplicationHealthcheckIntervalInvalid) ||
		errors.Is(err, application.ErrApplicationHealthcheckTimeoutInvalid) ||
		errors.Is(err, application.ErrApplicationHealthcheckRetriesInvalid) ||
		errors.Is(err, application.ErrApplicationHealthcheckStartPeriodInvalid) ||
		errors.Is(err, application.ErrApplicationDependencyServiceRequired) ||
		errors.Is(err, application.ErrApplicationDependencyServiceInvalid) ||
		errors.Is(err, application.ErrApplicationDependencyServiceNotFound) ||
		errors.Is(err, application.ErrApplicationDependencyConditionInvalid) ||
		errors.Is(err, application.ErrApplicationDependencyDuplicate) ||
		errors.Is(err, application.ErrApplicationDependencySelf) ||
		errors.Is(err, application.ErrApplicationRestartPolicyInvalid) ||
		errors.Is(err, application.ErrApplicationPortMappingInvalid) ||
		errors.Is(err, application.ErrApplicationVolumeSourceRequired) ||
		errors.Is(err, application.ErrApplicationVolumeSourceInvalid) ||
		errors.Is(err, application.ErrApplicationVolumeTargetRequired) ||
		errors.Is(err, application.ErrApplicationVolumeTargetInvalid) ||
		errors.Is(err, application.ErrApplicationVolumeOptionsInvalid)
}
