package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
	"redlaunch/internal/github"
	"redlaunch/internal/version"
)

const (
	updateCheckCacheTTL = 10 * time.Minute
	updateCheckErrorTTL = time.Minute
	updateMaxImageBytes = int64(1024 * 1024 * 1024)
	updateCheckTimeout  = 20 * time.Second
	updateDownloadLimit = 15 * time.Minute
	updateLoadTimeout   = 5 * time.Minute
	updateHealthTimeout = 2 * time.Minute
)

type releaseSource interface {
	Latest(context.Context) (application.ReleaseInfo, error)
	Download(context.Context, application.ReleaseAsset, string, string, int64) error
}

type updateDocker interface {
	LoadImage(context.Context, string) error
	InspectImage(context.Context, string) (compose.UpdateImageInfo, error)
	TagImage(context.Context, string, string) error
	ContainerExists(context.Context, string) (bool, error)
	RemoveContainer(context.Context, string) error
	InspectContainer(context.Context, string) (compose.UpdateContainerInfo, error)
	ComposeConfig(context.Context, string, string, string, string) ([]byte, error)
	RecreateApp(context.Context, string, string, string, string) error
	StartUpdateHelper(context.Context, string, string) error
	ExecHealth(context.Context, string, string) ([]byte, error)
}

// UpdateOptions configures the release-based updater.
type UpdateOptions struct {
	ProjectsRoot     string
	Directory        string
	DockerSocket     string
	RuntimeImage     string
	ManagerContainer string
	Docker           updateDocker
	ReleaseSource    releaseSource
	Arch             string
	Version          string
	DeferMutations   bool
	Now              func() time.Time
}

// UpdateService checks GitHub Releases and installs verified prebuilt images.
type UpdateService struct {
	projectsRoot     string
	directory        string
	dockerSocket     string
	runtimeImage     string
	managerContainer string
	helperName       string
	docker           updateDocker
	releases         releaseSource
	store            *updateStateStore
	arch             string
	currentVersion   string
	currentContract  int
	now              func() time.Time

	admission     chan struct{}
	admissionOnce sync.Once

	mu       sync.Mutex
	cached   application.UpdateStatus
	cacheAt  time.Time
	cacheErr error
}

// NewUpdateService constructs the release updater.
func NewUpdateService(options UpdateOptions) (*UpdateService, error) {
	projectsRoot := strings.TrimSpace(options.ProjectsRoot)
	directory := strings.TrimSpace(options.Directory)
	if projectsRoot == "" {
		return nil, errors.New("projects root is required")
	}
	if directory == "" {
		return nil, errors.New("Redlaunch directory is required")
	}
	absoluteProjectsRoot, err := filepath.Abs(projectsRoot)
	if err != nil {
		return nil, errors.New("projects root is invalid")
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("Redlaunch directory is invalid")
	}
	projectsRoot = absoluteProjectsRoot
	directory = absoluteDirectory
	docker := options.Docker
	if docker == nil {
		docker = compose.CommandRunner{}
	}
	releases := options.ReleaseSource
	if releases == nil {
		releases = github.NewClient()
	}
	arch := strings.TrimSpace(options.Arch)
	if arch == "" {
		arch = runtime.GOARCH
	}
	runtimeImage := strings.TrimSpace(options.RuntimeImage)
	if runtimeImage == "" {
		runtimeImage = "redlaunch:local"
	}
	container := strings.TrimSpace(options.ManagerContainer)
	if container == "" {
		container = "redbolt-redlaunch"
	}
	dockerSocket := strings.TrimSpace(options.DockerSocket)
	if dockerSocket == "" {
		dockerSocket = "/var/run/docker.sock"
	}
	store, err := newUpdateStateStore(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("prepare update state: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	current := version.Current()
	if strings.TrimSpace(options.Version) != "" {
		current.Version = strings.TrimSpace(options.Version)
	}
	admission := make(chan struct{})
	service := &UpdateService{
		projectsRoot:     projectsRoot,
		directory:        directory,
		dockerSocket:     dockerSocket,
		runtimeImage:     runtimeImage,
		managerContainer: container,
		helperName:       updateHelperName,
		docker:           docker,
		releases:         releases,
		store:            store,
		arch:             arch,
		currentVersion:   current.Version,
		currentContract:  current.DeploymentContract,
		now:              now,
		admission:        admission,
	}
	if !options.DeferMutations {
		service.markAdmitted()
	}
	return service, nil
}

// WaitForStartup blocks installation mutations until startup reconciliation
// has resolved any interrupted operation.
func (s *UpdateService) WaitForStartup(ctx context.Context) error {
	return s.allowMutations(ctx)
}

// allowMutations blocks until startup reconciliation has finished. Read-only
// release checks remain available while admission is deferred.
func (s *UpdateService) allowMutations(ctx context.Context) error {
	if s == nil || s.admission == nil {
		return nil
	}
	select {
	case <-s.admission:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *UpdateService) markAdmitted() {
	if s == nil || s.admission == nil {
		return
	}
	s.admissionOnce.Do(func() { close(s.admission) })
}

// CurrentVersion returns the running build's version.
func (s *UpdateService) CurrentVersion() string {
	if s == nil {
		return version.Current().Version
	}
	return s.currentVersion
}

// UpdateInProgress reports whether an installation lock is currently held.
func (s *UpdateService) UpdateInProgress() bool {
	return s != nil && s.store != nil && s.store.operationRunning()
}

// UpdateState returns the last persisted operation, if any.
func (s *UpdateService) UpdateState(context.Context) (application.UpdateState, bool, error) {
	if s == nil || s.store == nil {
		return application.UpdateState{}, false, nil
	}
	return s.store.readState()
}

// CheckForUpdate queries GitHub Releases and caches the result briefly.
func (s *UpdateService) CheckForUpdate(ctx context.Context) (application.UpdateStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.releases == nil || s.store == nil {
		return application.UpdateStatus{}, application.ErrUpdateUnavailable
	}
	status := application.UpdateStatus{
		CurrentVersion:   s.currentVersion,
		DevelopmentBuild: !isStableVersion(s.currentVersion),
	}
	if status.DevelopmentBuild {
		status.Message = "Development builds do not use release-based updates."
		return status, nil
	}

	s.mu.Lock()
	if !s.cacheAt.IsZero() {
		ttl := updateCheckCacheTTL
		if s.cacheErr != nil {
			ttl = updateCheckErrorTTL
		}
		if s.now().Sub(s.cacheAt) < ttl {
			cached := s.cached
			err := s.cacheErr
			s.mu.Unlock()
			return cached, err
		}
	}
	s.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	release, err := s.releases.Latest(checkCtx)
	if err != nil {
		status.Message = "The latest Redlaunch release could not be checked."
		s.cache(status, err)
		return status, err
	}
	status.LatestVersion = release.Version
	status.LatestURL = release.HTMLURL
	status.PublishedAt = release.PublishedAt
	status.Release = &release
	status.Compatible = release.Manifest.DeploymentContract == s.currentContract
	if !status.Compatible {
		status.Message = "The latest release requires a different Redlaunch deployment layout."
		s.cache(status, nil)
		return status, nil
	}
	newer, err := application.IsNewerRelease(s.currentVersion, release.Version)
	if err != nil {
		status.Message = "The installed Redlaunch version cannot be compared with the latest release."
		s.cache(status, err)
		return status, err
	}
	if newer {
		if _, err := validateReleaseImage(release.Manifest, s.arch); err != nil {
			status.Compatible = false
			status.Message = "The latest release has no image for this server architecture."
			s.cache(status, nil)
			return status, nil
		}
		status.UpdateAvailable = true
		status.Message = "A newer Redlaunch release is available."
	} else {
		status.Message = "Redlaunch is up to date."
	}
	s.cache(status, nil)
	return status, nil
}

func (s *UpdateService) validateInstallDirectory() error {
	info, err := os.Lstat(s.directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("installation directory is not a regular directory")
	}
	_, err = findUpdateComposeFile(s.directory)
	return err
}

func isStableVersion(value string) bool {
	_, err := application.ParseReleaseVersion(value)
	return err == nil
}

func (s *UpdateService) cache(status application.UpdateStatus, err error) {
	s.mu.Lock()
	s.cached = status
	s.cacheAt = s.now()
	s.cacheErr = err
	s.mu.Unlock()
}

// QueueReleaseUpdateWithProgress installs the release selected by CheckForUpdate.
func (s *UpdateService) QueueReleaseUpdateWithProgress(ctx context.Context, status application.UpdateStatus, progress func(stage, message string)) error {
	return s.queueReleaseUpdate(ctx, "", status, progress)
}

// QueueReleaseUpdateWithOperationID installs a release using a caller-supplied
// operation ID. The HTTP layer uses the tracked job ID so progress can be bound
// to the durable installation operation across the manager restart.
func (s *UpdateService) QueueReleaseUpdateWithOperationID(ctx context.Context, operationID string, status application.UpdateStatus, progress func(stage, message string)) error {
	return s.queueReleaseUpdate(ctx, operationID, status, progress)
}

func (s *UpdateService) queueReleaseUpdate(ctx context.Context, operationID string, status application.UpdateStatus, progress func(stage, message string)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.store == nil || s.docker == nil {
		return application.ErrUpdateUnavailable
	}
	if err := s.allowMutations(ctx); err != nil {
		return err
	}
	if status.Release == nil || !status.UpdateAvailable || !status.Compatible {
		return application.ErrReleaseIncompatible
	}
	if _, err := application.ParseReleaseVersion(status.Release.Version); err != nil {
		return application.ErrReleaseIncompatible
	}
	if strings.Contains(s.runtimeImage, "@") {
		return application.ErrReleaseIncompatible
	}
	if !isStableVersion(s.currentVersion) {
		return application.ErrUpdateUnavailable
	}
	if err := s.validateInstallDirectory(); err != nil {
		return fmt.Errorf("%w: %v", application.ErrReleaseIncompatible, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Acquire the shared installation lock before inspecting or touching the
	// helper. Removing or starting a helper before holding exclusion lets a
	// concurrent operation start its own helper in between.
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		var err error
		operationID, err = newUpdateOperationID()
		if err != nil {
			return err
		}
	} else if !validUpdateOperationID(operationID) {
		return application.ErrUpdateUnavailable
	}
	if err := s.store.acquire(operationID, "manager"); err != nil {
		return err
	}
	lockHeld := true
	releaseLock := func() {
		if lockHeld {
			_ = s.store.release(operationID)
			lockHeld = false
		}
	}
	running, err := s.helperRunning(ctx)
	if err != nil {
		releaseLock()
		return application.ErrUpdateUnavailable
	}
	if running {
		releaseLock()
		return application.ErrUpdateBusy
	}
	if err := s.removeStoppedHelper(ctx); err != nil {
		releaseLock()
		return err
	}
	release := *status.Release
	state := application.UpdateState{
		Schema:             updateStateSchema,
		Revision:           1,
		OperationID:        operationID,
		Owner:              "manager",
		Type:               updateOperationType,
		Phase:              application.UpdatePhasePreparing,
		Version:            release.Version,
		Commit:             release.Manifest.Commit,
		DeploymentContract: release.Manifest.DeploymentContract,
		RuntimeImage:       s.runtimeImage,
		InstallDir:         s.directory,
		HelperName:         s.helperName,
		StartedAt:          s.now().UTC(),
		UpdatedAt:          s.now().UTC(),
	}
	if err := validateUpdatePathBeforeCreate(s.projectsRoot, s.store.downloadDir(operationID)); err != nil {
		releaseLock()
		return application.ErrReleaseIncompatible
	}
	if err := s.store.writeState(&state); err != nil {
		releaseLock()
		if errors.Is(err, application.ErrUpdateBusy) {
			return application.ErrUpdateBusy
		}
		return application.ErrUpdateUnavailable
	}
	reportUpdateProgress(progress, "check", "Validating the latest Redlaunch release")
	image, err := validateReleaseImage(release.Manifest, s.arch)
	if err != nil {
		state.Phase = application.UpdatePhaseFailed
		state.Error = "The release has no image for this architecture."
		state.FinishedAt = s.now().UTC()
		return s.failState(&state, &lockHeld, "The release has no image for this architecture.", err)
	}
	if !validUpdateAssetName(image.Asset) {
		return s.failState(&state, &lockHeld, "The release image asset name is invalid.", application.ErrReleaseIncompatible)
	}
	asset, ok := releaseAsset(release, image.Asset)
	if !ok {
		return s.failState(&state, &lockHeld, "The release image asset is missing.", application.ErrReleaseUnavailable)
	}
	container, err := s.docker.InspectContainer(ctx, s.managerContainer)
	if err != nil || !container.State.Running || strings.TrimSpace(container.Image) == "" {
		return s.failState(&state, &lockHeld, "The running Redlaunch container could not be inspected.", application.ErrReleaseUnavailable)
	}
	state.PreviousImageID = container.Image
	state.ManagerContainer = s.managerContainer
	composeFile, err := findUpdateComposeFile(s.directory)
	if err != nil {
		return s.failState(&state, &lockHeld, "The installation Compose file could not be found.", application.ErrReleaseIncompatible)
	}
	state.ComposeFile = composeFile
	state.ProjectName = managerProjectName(container, s.directory)
	if err := validateLiveComposeFiles(container, composeFile, s.store.appOverridePath()); err != nil {
		return s.failState(&state, &lockHeld, "The installation Compose files are not supported by this release updater.", application.ErrReleaseIncompatible)
	}
	if err := s.validateDeployment(ctx, state, container); err != nil {
		return s.failState(&state, &lockHeld, "The installation layout is not supported by this release updater: "+err.Error(), application.ErrReleaseIncompatible)
	}

	state.Phase = application.UpdatePhaseDownloading
	state.DownloadPath = filepath.Join(s.store.downloadDir(operationID), image.Asset)
	if err := validateUpdatePathBeforeCreate(s.projectsRoot, state.DownloadPath); err != nil {
		return s.failState(&state, &lockHeld, "The release image download path is invalid.", application.ErrReleaseIncompatible)
	}
	if err := os.MkdirAll(filepath.Dir(state.DownloadPath), 0o700); err != nil {
		return s.failState(&state, &lockHeld, "The update download directory could not be prepared.", application.ErrUpdateUnavailable)
	}
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	reportUpdateProgress(progress, "download", "Downloading the verified Redlaunch release image")
	downloadCtx, cancel := context.WithTimeout(ctx, updateDownloadLimit)
	err = s.releases.Download(downloadCtx, asset, state.DownloadPath, image.SHA256, updateMaxImageBytes)
	cancel()
	if err != nil {
		return s.failState(&state, &lockHeld, "The release image could not be downloaded.", application.ErrReleaseUnavailable)
	}
	reportUpdateProgress(progress, "verify", "Verifying the release image")
	if err := validateImageArchive(ctx, state.DownloadPath, image.Tag, release.Version, release.Manifest.Commit, release.Manifest.DeploymentContract); err != nil {
		return s.failState(&state, &lockHeld, "The release image archive is invalid.", application.ErrReleaseUnavailable)
	}
	state.Phase = application.UpdatePhaseVerified
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	reportUpdateProgress(progress, "load", "Loading the verified Redlaunch image")
	loadCtx, cancel := context.WithTimeout(ctx, updateLoadTimeout)
	err = s.docker.LoadImage(loadCtx, state.DownloadPath)
	cancel()
	if err != nil {
		return s.failState(&state, &lockHeld, "The release image could not be loaded.", application.ErrReleaseUnavailable)
	}
	loaded, err := s.docker.InspectImage(ctx, image.Tag)
	if err != nil {
		return s.failState(&state, &lockHeld, "The loaded release image could not be inspected.", application.ErrReleaseUnavailable)
	}
	if err := validateLoadedImage(loaded, image, release.Version, release.Manifest.Commit, release.Manifest.DeploymentContract); err != nil {
		return s.failState(&state, &lockHeld, "The loaded image does not match the release contract.", application.ErrReleaseIncompatible)
	}
	state.TargetImageID = loaded.ID
	state.Phase = application.UpdatePhaseLoaded
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	if err := s.writeHelperProject(state); err != nil {
		return s.failState(&state, &lockHeld, "The update helper configuration could not be prepared.", application.ErrUpdateUnavailable)
	}
	reportUpdateProgress(progress, "install", "Starting the Redlaunch update helper")
	state.Phase = application.UpdatePhaseRestartQueued
	state.Owner = "helper"
	state.UpdatedAt = s.now().UTC()
	if err := s.store.writeState(&state); err != nil {
		return application.ErrUpdateUnavailable
	}
	if err := s.docker.StartUpdateHelper(ctx, s.store.helperDir(), updateHelperProjectName); err != nil {
		// The helper may have started despite the error (client timeout after
		// Docker accepted the request). Re-read before overwriting so helper
		// progress is never clobbered.
		if current, ok, readErr := s.store.readState(); readErr == nil && ok && current.OperationID == state.OperationID && current.Phase != application.UpdatePhaseRestartQueued {
			return application.ErrUpdateUncertain
		}
		state.Owner = "manager"
		state.Phase = application.UpdatePhaseUncertain
		state.Error = "The Redlaunch update helper could not be confirmed as started."
		state.UpdatedAt = s.now().UTC()
		if err := s.store.writeState(&state); err != nil {
			return application.ErrUpdateUncertain
		}
		return application.ErrUpdateUncertain
	}
	lockHeld = false
	return nil
}

func (s *UpdateService) failState(state *application.UpdateState, lockHeld *bool, message string, cause error) error {
	state.Phase = application.UpdatePhaseFailed
	state.Owner = "manager"
	state.Error = message
	state.FinishedAt = s.now().UTC()
	if err := s.store.writeState(state); err != nil {
		// Do not delete downloaded/override artifacts or release exclusion
		// when the failure could not be persisted.
		return cause
	}
	s.cleanupDownload(*state)
	*lockHeld = false
	_ = s.store.release(state.OperationID)
	return cause
}

func reportUpdateProgress(progress func(stage, message string), stage, message string) {
	if progress != nil {
		progress(stage, message)
	}
}

// validUpdateAssetName keeps a release asset to a single file name before it
// is joined to the private download directory.
func validUpdateAssetName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) &&
		filepath.Base(name) == name
}

func releaseAsset(release application.ReleaseInfo, name string) (application.ReleaseAsset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return application.ReleaseAsset{}, false
}

func newUpdateOperationID() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func findUpdateComposeFile(directory string) (string, error) {
	for _, name := range []string{"docker-compose.yml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("Compose file is not a regular file")
		}
		return path, nil
	}
	return "", errors.New("Compose file was not found")
}

func managerProjectName(container compose.UpdateContainerInfo, directory string) string {
	if project := strings.TrimSpace(container.Config.Labels["com.docker.compose.project"]); project != "" {
		return project
	}
	return compose.ProjectName(directory)
}

func (s *UpdateService) removeStoppedHelper(ctx context.Context) error {
	exists, err := s.docker.ContainerExists(ctx, s.helperName)
	if err != nil {
		return application.ErrUpdateUnavailable
	}
	if !exists {
		return nil
	}
	info, err := s.docker.InspectContainer(ctx, s.helperName)
	if err != nil {
		// Fail closed: an uninspectable helper may be starting concurrently.
		return application.ErrUpdateBusy
	}
	if info.State.Running {
		return application.ErrUpdateBusy
	}
	labels := info.Config.Labels
	if labels["redlaunch.managed"] != "true" || labels["redlaunch.updater"] != "true" {
		return errors.New("update helper name is owned by another container")
	}
	if name := strings.TrimPrefix(strings.TrimSpace(info.Name), "/"); name != "" && name != s.helperName {
		return errors.New("update helper name is owned by another container")
	}
	if err := s.docker.RemoveContainer(ctx, s.helperName); err != nil {
		return application.ErrUpdateBusy
	}
	return nil
}

type updateMount struct {
	Type   string
	Source string
}

func (s *UpdateService) validateDeployment(ctx context.Context, state application.UpdateState, live compose.UpdateContainerInfo) error {
	incompatible := func(reason string) error {
		return fmt.Errorf("%w: %s", application.ErrReleaseIncompatible, reason)
	}
	if state.ComposeFile == "" || state.ProjectName == "" {
		return incompatible("the Compose file or project name is missing")
	}
	resolved, err := s.docker.ComposeConfig(ctx, s.directory, state.ProjectName, state.ComposeFile, "")
	if err != nil {
		return err
	}
	var project struct {
		Name    string `json:"name"`
		Volumes map[string]struct {
			Name string `json:"name"`
		} `json:"volumes"`
		Services map[string]struct {
			Image         string            `json:"image"`
			ContainerName string            `json:"container_name"`
			Command       json.RawMessage   `json:"command"`
			Entrypoint    json.RawMessage   `json:"entrypoint"`
			Privileged    *bool             `json:"privileged"`
			NetworkMode   string            `json:"network_mode"`
			Pid           string            `json:"pid"`
			Ipc           string            `json:"ipc"`
			Labels        map[string]string `json:"labels"`
			Environment   map[string]string `json:"environment"`
			Volumes       []struct {
				Type   string `json:"type"`
				Source string `json:"source"`
				Target string `json:"target"`
				Volume struct {
					Subpath string `json:"subpath"`
				} `json:"volume"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(resolved, &project); err != nil {
		return incompatible("the resolved Compose configuration is invalid")
	}
	if len(project.Services) != 1 {
		return incompatible("the Compose project must contain exactly one service")
	}
	app, ok := project.Services["app"]
	if !ok {
		return incompatible("the Compose project has no app service")
	}
	if project.Name != "" && state.ProjectName != "" && project.Name != state.ProjectName {
		return incompatible("the Compose project name changed")
	}
	if app.ContainerName != "" && state.ManagerContainer != "" && app.ContainerName != state.ManagerContainer {
		return incompatible("the app container name changed")
	}
	if app.Labels["redlaunch.managed"] != "true" {
		return incompatible("the app service is not labeled as a managed container")
	}
	if isComposeOverrideSet(app.Command) || isComposeOverrideSet(app.Entrypoint) {
		return incompatible("the app service overrides command or entrypoint")
	}
	if app.Privileged != nil && *app.Privileged {
		return incompatible("the app service is privileged")
	}
	if app.NetworkMode == "host" || app.Pid == "host" || app.Ipc == "host" {
		return incompatible("the app service uses a host namespace")
	}
	if app.Environment["HTTP_ADDR"] == "" {
		return incompatible("the app service has no HTTP_ADDR")
	}
	if strings.TrimSpace(app.Image) == "" {
		return incompatible("the app service has no image")
	}
	if strings.Contains(app.Image, "@") {
		return incompatible("the app image is digest-pinned")
	}
	if !strings.HasPrefix(strings.TrimSpace(app.Image), "sha256:") && strings.TrimSpace(app.Image) != s.runtimeImage {
		return incompatible("the app image does not match the runtime image")
	}

	// The database must live inside the persistent /data mount. A database
	// path elsewhere would be lost when the manager container is recreated.
	dbPath := strings.TrimSpace(app.Environment["DB_PATH"])
	if dbPath == "" || !filepath.IsAbs(dbPath) || filepath.Clean(dbPath) == "/data" || !pathContained("/data", dbPath) {
		return incompatible("the database path is not inside the persistent /data mount")
	}
	liveEnv := make(map[string]string, len(live.Config.Env))
	for _, entry := range live.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			liveEnv[key] = value
		}
	}
	for _, key := range []string{"DB_PATH", "PROJECTS_ROOT", "REDLAUNCH_DIR"} {
		if liveEnv[key] != "" && strings.TrimSpace(app.Environment[key]) != "" && liveEnv[key] != app.Environment[key] {
			return incompatible("the live " + key + " does not match the Compose configuration")
		}
	}
	for key, expected := range map[string]string{
		"REDLAUNCH_IMAGE":          s.runtimeImage,
		"REDLAUNCH_CONTAINER_NAME": s.managerContainer,
	} {
		if value := strings.TrimSpace(app.Environment[key]); value != "" && value != expected {
			return incompatible("the Compose " + key + " does not match the updater")
		}
		if value := strings.TrimSpace(liveEnv[key]); value != "" && value != expected {
			return incompatible("the live " + key + " does not match the updater")
		}
	}

	composeMounts := make(map[string]updateMount, len(app.Volumes))
	for _, volume := range app.Volumes {
		target := filepath.Clean(strings.TrimSpace(volume.Target))
		mountType := strings.TrimSpace(volume.Type)
		source := strings.TrimSpace(volume.Source)
		if target == "" || target == "." || (mountType != "volume" && mountType != "bind") {
			return incompatible("the app service has an unsupported mount")
		}
		switch mountType {
		case "bind":
			if !filepath.IsAbs(source) {
				return incompatible("a bind mount source is not absolute")
			}
		case "volume":
			// A subpath redirects the mount inside the volume without changing
			// its name. Docker does not report it in container inspection, so
			// the updater cannot prove recreation preserves the database
			// directory.
			if strings.TrimSpace(volume.Volume.Subpath) != "" {
				return incompatible("the app service uses a volume subpath")
			}
			// Compose's resolved configuration always records the concrete
			// Docker volume name in the project-level volumes section. Compare
			// that identity, not the project-scoped source key: replacing
			// /data with another named volume must be rejected.
			resolved, ok := project.Volumes[source]
			if !ok || strings.TrimSpace(resolved.Name) == "" {
				return incompatible("a named volume is missing its Docker identity")
			}
			source = strings.TrimSpace(resolved.Name)
		}
		composeMounts[target] = updateMount{Type: mountType, Source: source}
	}
	liveMounts := make(map[string]updateMount, len(live.Mounts))
	for _, mount := range live.Mounts {
		destination := filepath.Clean(strings.TrimSpace(mount.Destination))
		if isDockerManagedMount(destination) {
			continue
		}
		mountType := strings.TrimSpace(mount.Type)
		source := strings.TrimSpace(mount.Source)
		if mountType == "volume" {
			// Docker reports the host path in Source for volume mounts. The
			// Name field is the persistent volume identity.
			source = strings.TrimSpace(mount.Name)
			if source == "" {
				return incompatible("a live named volume is missing its Docker identity")
			}
		}
		if mountType == "bind" {
			source = normalizeDockerDesktopSocketMount(source, destination, composeMounts, s.dockerSocket)
		}
		liveMounts[destination] = updateMount{Type: mountType, Source: source}
	}
	// Recreation must preserve every host-visible mount source and target. A
	// changed bind source can silently switch the database directory, Docker
	// socket, or installation checkout.
	if len(composeMounts) != len(liveMounts) {
		return incompatible("the live mounts do not match the Compose configuration")
	}
	for target, expected := range composeMounts {
		actual, ok := liveMounts[target]
		if !ok || !equivalentUpdateMount(expected, actual) {
			return incompatible("the live mount for " + target + " changed")
		}
	}
	// The manager database, Docker socket, installation directory, and
	// projects root must survive recreation. An installation missing any of
	// these mounts is customized beyond what the release updater supports.
	if _, ok := composeMounts["/data"]; !ok {
		return incompatible("the persistent /data mount is missing")
	}
	socketTarget := s.dockerSocket
	if _, ok := composeMounts[socketTarget]; !ok {
		socketTarget = "/var/run/docker.sock"
	}
	if _, ok := composeMounts[socketTarget]; !ok {
		return incompatible("the Docker socket mount is missing")
	}
	if _, ok := composeMounts[filepath.Clean(s.directory)]; !ok {
		return incompatible("the installation directory mount is missing")
	}
	if _, ok := composeMounts[filepath.Clean(s.projectsRoot)]; !ok {
		return incompatible("the projects root mount is missing")
	}
	return nil
}

// normalizeDockerDesktopSocketMount accepts the proxy path Docker Desktop
// reports when Compose ran inside a container. It only does so while the
// Compose configuration still declares the real Docker socket, so a changed
// socket source remains a hard failure.
func normalizeDockerDesktopSocketMount(source, destination string, composeMounts map[string]updateMount, dockerSocket string) string {
	const proxy = "/run/host-services/docker.proxy.sock"
	if source != proxy {
		return source
	}
	expected, ok := composeMounts[destination]
	if !ok || expected.Type != "bind" || (expected.Source != "/var/run/docker.sock" && expected.Source != filepath.Clean(dockerSocket)) {
		return source
	}
	if _, err := os.Stat(source); err == nil {
		return source
	}
	return expected.Source
}

func equivalentUpdateMount(left, right updateMount) bool {
	if left.Type != right.Type {
		return false
	}
	if left.Type == "volume" {
		return left.Source != "" && left.Source == right.Source
	}
	if left.Type != "bind" {
		return false
	}
	return sameUpdatePath(left.Source, right.Source)
}

func sameUpdatePath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	cleanLeft, cleanRight := filepath.Clean(left), filepath.Clean(right)
	if cleanLeft == cleanRight {
		return true
	}
	if dockerDesktopHostMountEquivalent(cleanLeft, cleanRight) {
		return true
	}
	resolvedLeft, errLeft := filepath.EvalSymlinks(cleanLeft)
	resolvedRight, errRight := filepath.EvalSymlinks(cleanRight)
	return errLeft == nil && errRight == nil && resolvedLeft == resolvedRight
}

// dockerDesktopHostMountEquivalent reconciles Docker Desktop's /host_mnt/<host
// path> representation for bind mounts created by a client running inside a
// container. It only applies when one side has the prefix, the other does not,
// and the prefixed path does not exist on this host (so a real /host_mnt tree
// is never silently equated with an unprefixed path).
func dockerDesktopHostMountEquivalent(left, right string) bool {
	const prefix = "/host_mnt/"
	var prefixed, plain string
	switch {
	case strings.HasPrefix(left, prefix) && !strings.HasPrefix(right, prefix):
		prefixed, plain = left, right
	case strings.HasPrefix(right, prefix) && !strings.HasPrefix(left, prefix):
		prefixed, plain = right, left
	default:
		return false
	}
	if !dockerDesktopHostPath(plain) {
		return false
	}
	if _, err := os.Stat(prefixed); err == nil {
		return false
	}
	return strings.TrimPrefix(prefixed, "/host_mnt") == plain
}

// dockerDesktopHostPath limits the /host_mnt normalization to paths Docker
// Desktop presents from host file sharing, rather than arbitrary host paths
// such as /etc.
func dockerDesktopHostPath(path string) bool {
	for _, prefix := range []string{"/Users/", "/private/", "/Volumes/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isDockerManagedMount(destination string) bool {
	switch destination {
	case "/etc/hosts", "/etc/hostname", "/etc/resolv.conf", "/dev/termination-log":
		return true
	}
	return false
}

func validateLiveComposeFiles(container compose.UpdateContainerInfo, composeFile, overrideFile string) error {
	files := strings.TrimSpace(container.Config.Labels["com.docker.compose.project.config_files"])
	if files == "" {
		return nil
	}
	// Compose records every -f file used to start the running container. The
	// supported layout is either the installation Compose file alone or that
	// file followed by the updater-owned image override written by a previous
	// update. Any other file is an unsupported customization.
	parts := strings.Split(files, ",")
	for index := range parts {
		parts[index] = filepath.Clean(strings.TrimSpace(parts[index]))
	}
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || parts[0] != filepath.Clean(composeFile) {
		return application.ErrReleaseIncompatible
	}
	if len(parts) == 2 && parts[1] != filepath.Clean(overrideFile) {
		return application.ErrReleaseIncompatible
	}
	return nil
}

func isComposeOverrideSet(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	// JSON null, empty string, and empty array all mean "no override".
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strings.TrimSpace(asString) != ""
	}
	var asSlice []any
	if err := json.Unmarshal(raw, &asSlice); err == nil {
		return len(asSlice) != 0
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err == nil {
		return len(asMap) != 0
	}
	return true
}

func updateHealthEndpoint(container compose.UpdateContainerInfo) (string, error) {
	address := ""
	for _, entry := range container.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "HTTP_ADDR" {
			address = strings.TrimSpace(value)
			break
		}
	}
	if address == "" {
		return "", errors.New("HTTP_ADDR is not configured")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if strings.HasPrefix(address, ":") {
			port = strings.TrimPrefix(address, ":")
		} else {
			return "", errors.New("HTTP_ADDR is invalid")
		}
	}
	if port == "" {
		return "", errors.New("HTTP_ADDR port is invalid")
	}
	_ = host
	return "http://127.0.0.1:" + port + "/healthz", nil
}

func updateStateComplete(state *application.UpdateState, targetImageID, version string, now time.Time) {
	state.Phase = application.UpdatePhaseComplete
	state.Owner = "helper"
	state.TargetImageID = targetImageID
	state.Version = version
	state.FinishedAt = now.UTC()
	state.UpdatedAt = now.UTC()
	state.Error = ""
}

// UpdateStateForTests is intentionally not exported beyond this file; tests
// exercise the service through its public methods.
