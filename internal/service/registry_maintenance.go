package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

const (
	// RegistryMaintenanceInterval is the in-memory automatic retention and
	// garbage-collection cadence. Manual actions reset the corresponding timer.
	RegistryMaintenanceInterval = 24 * time.Hour
	// RegistryMaintenanceTimeout bounds one automatic read-only maintenance
	// window. Manual actions use the tracked job timeout instead.
	RegistryMaintenanceTimeout = 30 * time.Minute

	registryMaintenanceStateFile       = "state.json"
	registryMaintenanceStateVersion    = 1
	registryMaintenanceCleanupTimeout  = 2 * time.Minute
	registryMaintenanceReadyTimeout    = 30 * time.Second
	registryMaintenanceMaxStateSize    = 2 << 20
	registryMaintenanceProbeRepository = "redlaunch-maintenance-probe"
)

const (
	registryMaintenancePhasePrepared      = "prepared"
	registryMaintenancePhaseReadOnly      = "read-only"
	registryMaintenancePhaseCollecting    = "collecting"
	registryMaintenancePhaseRestoring     = "restoring"
	registryMaintenancePhaseRestoreVerify = "restore-verify"
)

// RegistryVersionStorage operates on manifests, independently of their tags.
// References returns the child manifests of an image index (if any).
type RegistryVersionStorage interface {
	RegistryManifestReferences(context.Context, string, string, string) ([]string, error)
	DeleteRegistryVersion(context.Context, string, string, string) error
}

type registryMaintenanceBackend interface {
	RegistryContainerTarget(context.Context, string) (compose.RegistryMaintenanceTarget, error)
	ApplyRegistryConfig(context.Context, string, string, []byte) error
	RestartRegistryContainer(context.Context, string) error
	WaitRegistryReady(context.Context, string, time.Duration) error
	RegistryWritesAccepted(context.Context, string, string) (bool, error)
	RemoveRegistryRepository(context.Context, string, string) error
	GarbageCollectRegistry(context.Context, string, string) error
	RegistryStorageBytes(context.Context, string) (int64, bool)
}

// RegistryMaintenanceOptions selects the operations for one read-only window.
// Scheduled requests re-check the in-memory schedule after taking the
// maintenance lock so a manual run cannot be duplicated by the worker.
type RegistryMaintenanceOptions struct {
	Retention         bool
	GarbageCollection bool
	Protected         map[string]bool
	Scheduled         bool
}

// RegistryMaintenanceResult reports the observable outcome of one maintenance
// window. Partial results are returned with an error when cleanup completed but
// an operation failed.
type RegistryMaintenanceResult struct {
	DeletedVersions            int
	RetentionCompleted         bool
	GarbageCollectionCompleted bool
	ReclaimedBytes             int64
	ReclaimedBytesKnown        bool
}

type registryMaintenanceState struct {
	Version        int       `json:"version"`
	Container      string    `json:"container"`
	ContainerID    string    `json:"container_id"`
	ConfigPath     string    `json:"config_path"`
	OriginalConfig []byte    `json:"original_config"`
	ReadOnlyConfig []byte    `json:"read_only_config"`
	Operation      string    `json:"operation"`
	Phase          string    `json:"phase"`
	StartedAt      time.Time `json:"started_at"`
}

// SetRetentionStore configures the shared automatic/manual retention policy.
func (s *RegistryService) SetRetentionStore(store RegistryRetentionStore) {
	if s == nil {
		return
	}
	s.retentionStore = store
}

// SetMaintenanceStateDir configures the durable recovery directory for
// read-only maintenance windows.
func (s *RegistryService) SetMaintenanceStateDir(directory string) error {
	if s == nil {
		return errors.New("registry service is required")
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("registry maintenance state directory is required")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("registry maintenance state directory must be an absolute clean path")
	}
	if err := ensureRegistryMaintenanceDirectory(directory); err != nil {
		return err
	}
	s.stateDir = directory
	return nil
}

// GetRetentionKeep returns the shared global keep count.
func (s *RegistryService) GetRetentionKeep(ctx context.Context) (int, error) {
	if s == nil || s.retentionStore == nil {
		return 0, errors.New("registry retention store is not configured")
	}
	return s.retentionStore.GetRegistryRetentionKeep(ctx)
}

// SetRetentionKeep persists the shared global keep count.
func (s *RegistryService) SetRetentionKeep(ctx context.Context, keep int) error {
	if s == nil || s.retentionStore == nil {
		return errors.New("registry retention store is not configured")
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	return s.retentionStore.SetRegistryRetentionKeep(ctx, keep)
}

// ListRetentionPolicies returns per-repository retention overrides.
func (s *RegistryService) ListRetentionPolicies(ctx context.Context) ([]application.RegistryRetentionPolicy, error) {
	if s == nil || s.retentionStore == nil {
		return nil, nil
	}
	return s.retentionStore.ListRegistryRetentionPolicies(ctx)
}

// SetRepositoryRetention persists one repository override.
func (s *RegistryService) SetRepositoryRetention(ctx context.Context, repository string, keep int) error {
	if s == nil || s.retentionStore == nil {
		return errors.New("registry retention store is not configured")
	}
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return err
	}
	if _, err := application.ValidateRegistryKeepCount(keep); err != nil {
		return err
	}
	return s.retentionStore.SetRegistryRetentionPolicy(ctx, repository, keep)
}

// DeleteRepositoryRetention removes one repository override.
func (s *RegistryService) DeleteRepositoryRetention(ctx context.Context, repository string) error {
	if s == nil || s.retentionStore == nil {
		return errors.New("registry retention store is not configured")
	}
	repository, err := application.ValidateRegistryRepository(repository)
	if err != nil {
		return err
	}
	return s.retentionStore.DeleteRegistryRetentionPolicy(ctx, repository)
}

// LastRetention reports the last fully restored automatic or manual retention.
func (s *RegistryService) LastRetention() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	return s.lastRetention
}

// LastGarbageCollection reports the last fully restored garbage collection.
func (s *RegistryService) LastGarbageCollection() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	return s.lastGarbage
}

// MaintenanceError reports a recovery failure that blocks maintenance.
func (s *RegistryService) MaintenanceError() error {
	if s == nil {
		return nil
	}
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	return s.maintenanceErr
}

func (s *RegistryService) setMaintenanceError(err error) {
	s.scheduleMu.Lock()
	s.maintenanceErr = err
	s.scheduleMu.Unlock()
}

// RecoverRegistryMaintenance restores a registry left in read-only mode by an
// interrupted maintenance window. It is safe to call repeatedly.
func (s *RegistryService) RecoverRegistryMaintenance(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	err := s.recoverRegistryMaintenanceLocked(ctx)
	if err != nil {
		s.setMaintenanceError(err)
		return err
	}
	s.setMaintenanceError(nil)
	return nil
}

func (s *RegistryService) recoverRegistryMaintenanceLocked(ctx context.Context) error {
	if s.stateDir == "" {
		return nil
	}
	state, ok, err := s.readMaintenanceState()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	backend, err := s.maintenanceBackend()
	if err != nil {
		return err
	}
	if state.Container != s.container {
		return errors.New("registry maintenance state belongs to another container")
	}
	if state.Version != registryMaintenanceStateVersion || len(state.OriginalConfig) == 0 || len(state.ReadOnlyConfig) == 0 {
		return errors.New("registry maintenance state is invalid")
	}
	// Restart first so a stopped container becomes inspectable and any garbage
	// collector that outlived its local Docker CLI is terminated. The current
	// configuration decides whether the container starts read-only or writable.
	if err := backend.RestartRegistryContainer(ctx, s.container); err != nil {
		return fmt.Errorf("restart registry during maintenance recovery: %w", err)
	}
	if err := backend.WaitRegistryReady(ctx, s.container, registryMaintenanceReadyTimeout); err != nil {
		return err
	}
	target, err := backend.RegistryContainerTarget(ctx, s.container)
	if err != nil {
		return fmt.Errorf("inspect registry during maintenance recovery: %w", err)
	}
	if target.ConfigPath != state.ConfigPath {
		return errors.New("registry configuration path changed during maintenance; refusing to overwrite it")
	}
	probeRepository := newRegistryMaintenanceProbe()
	current := normalizeRegistryConfig(target.Config)
	switch current {
	case normalizeRegistryConfig(state.ReadOnlyConfig):
		if err := backend.ApplyRegistryConfig(ctx, s.container, state.ConfigPath, state.OriginalConfig); err != nil {
			return fmt.Errorf("restore registry configuration: %w", err)
		}
	case normalizeRegistryConfig(state.OriginalConfig):
		// The configuration was restored before the process stopped.
	default:
		return errors.New("registry configuration changed during maintenance; refusing to overwrite it")
	}
	if err := backend.RestartRegistryContainer(ctx, s.container); err != nil {
		return err
	}
	if err := backend.WaitRegistryReady(ctx, s.container, registryMaintenanceReadyTimeout); err != nil {
		return err
	}
	if err := s.verifyRegistryWritable(ctx, backend, probeRepository); err != nil {
		return err
	}
	if err := s.removeMaintenanceState(); err != nil {
		return err
	}
	return nil
}

// ApplyMaintenance performs retention and/or garbage collection in one
// verified read-only window. It returns application.ErrRegistryBusy when
// another registry operation already holds the mutation lock.
func (s *RegistryService) ApplyMaintenance(ctx context.Context, options RegistryMaintenanceOptions) (result RegistryMaintenanceResult, err error) {
	if s == nil {
		return result, errors.New("registry service is not configured")
	}
	if s.MaintenanceError() != nil {
		return result, s.MaintenanceError()
	}
	if !s.mutations.TryLock() {
		return result, application.ErrRegistryBusy
	}
	defer s.mutations.Unlock()

	if options.Scheduled {
		options.Retention, options.GarbageCollection = s.dueMaintenance(options.Retention, options.GarbageCollection)
	}
	if !options.Retention && !options.GarbageCollection {
		return result, nil
	}
	if options.Retention && s.retentionStore == nil {
		return result, errors.New("registry retention store is not configured")
	}
	defaultKeep := application.DefaultRegistryRetentionKeep
	policyByRepository := map[string]int{}
	if options.Retention {
		defaultKeep, err = s.GetRetentionKeep(ctx)
		if err != nil {
			return result, fmt.Errorf("read registry retention settings: %w", err)
		}
		policies, policyErr := s.ListRetentionPolicies(ctx)
		if policyErr != nil {
			return result, fmt.Errorf("read registry retention policies: %w", policyErr)
		}
		for _, policy := range policies {
			if _, validateErr := application.ValidateRegistryKeepCount(policy.KeepCount); validateErr != nil {
				return result, fmt.Errorf("registry retention policy for %q is invalid", policy.Repository)
			}
			policyByRepository[policy.Repository] = policy.KeepCount
		}
	}

	backend, err := s.maintenanceBackend()
	if err != nil {
		return result, err
	}
	if err := s.recoverRegistryMaintenanceLocked(ctx); err != nil {
		s.setMaintenanceError(err)
		return result, err
	}
	target, err := backend.RegistryContainerTarget(ctx, s.container)
	if err != nil {
		return result, err
	}
	if !target.WasRunning {
		return result, errors.Join(application.ErrRegistryUnavailable, errors.New("registry container is not running"))
	}
	readOnlyConfig, err := compose.RegistryReadOnlyConfig(target.Config)
	if err != nil {
		return result, err
	}
	state := registryMaintenanceState{
		Version:        registryMaintenanceStateVersion,
		Container:      s.container,
		ContainerID:    target.ContainerID,
		ConfigPath:     target.ConfigPath,
		OriginalConfig: target.Config,
		ReadOnlyConfig: readOnlyConfig,
		Operation:      maintenanceOperation(options),
		Phase:          registryMaintenancePhasePrepared,
		StartedAt:      time.Now().UTC(),
	}
	if err := s.writeMaintenanceState(state); err != nil {
		return result, err
	}
	probeRepository := newRegistryMaintenanceProbe()
	gcUncertain := false
	cleanupDone := false
	defer func() {
		if cleanupDone {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), registryMaintenanceCleanupTimeout)
		defer cancel()
		cleanupErr := s.restoreRegistryAfterMaintenance(cleanupCtx, backend, state, probeRepository, gcUncertain)
		if cleanupErr != nil {
			s.setMaintenanceError(cleanupErr)
			err = errors.Join(err, cleanupErr)
			return
		}
		s.recordMaintenanceSuccess(result)
	}()

	if err := backend.ApplyRegistryConfig(ctx, s.container, state.ConfigPath, state.ReadOnlyConfig); err != nil {
		return result, err
	}
	state.Phase = registryMaintenancePhaseReadOnly
	if err := s.writeMaintenanceState(state); err != nil {
		return result, err
	}
	if err := backend.RestartRegistryContainer(ctx, s.container); err != nil {
		return result, err
	}
	if err := backend.WaitRegistryReady(ctx, s.container, registryMaintenanceReadyTimeout); err != nil {
		return result, err
	}
	if err := s.verifyRegistryReadOnly(ctx, backend, probeRepository); err != nil {
		return result, err
	}

	if options.Retention {
		deleted, retentionErr := s.retainVersionsLocked(ctx, defaultKeep, policyByRepository, options.Protected)
		result.DeletedVersions = deleted
		if retentionErr != nil {
			return result, retentionErr
		}
		result.RetentionCompleted = true
	}
	if options.GarbageCollection {
		before, beforeKnown := backend.RegistryStorageBytes(ctx, s.container)
		state.Phase = registryMaintenancePhaseCollecting
		if err := s.writeMaintenanceState(state); err != nil {
			gcUncertain = true
			return result, err
		}
		gcErr := backend.GarbageCollectRegistry(ctx, s.container, state.ConfigPath)
		if gcErr != nil {
			gcUncertain = true
			return result, gcErr
		}
		after, afterKnown := backend.RegistryStorageBytes(ctx, s.container)
		if beforeKnown && afterKnown && before >= after {
			result.ReclaimedBytes = before - after
			result.ReclaimedBytesKnown = true
		}
		result.GarbageCollectionCompleted = true
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), registryMaintenanceCleanupTimeout)
	cleanupErr := s.restoreRegistryAfterMaintenance(cleanupCtx, backend, state, probeRepository, false)
	cancel()
	cleanupDone = true
	if cleanupErr != nil {
		s.setMaintenanceError(cleanupErr)
		return result, cleanupErr
	}
	s.setMaintenanceError(nil)
	s.recordMaintenanceSuccess(result)
	return result, nil
}

func (s *RegistryService) recordMaintenanceSuccess(result RegistryMaintenanceResult) {
	now := time.Now()
	s.scheduleMu.Lock()
	if result.RetentionCompleted {
		s.lastRetention = now
	}
	if result.GarbageCollectionCompleted {
		s.lastGarbage = now
	}
	s.scheduleMu.Unlock()
}

func (s *RegistryService) maintenanceDue(now time.Time) (bool, bool) {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	retentionDue := s.lastRetention.IsZero() || now.Sub(s.lastRetention) >= RegistryMaintenanceInterval
	garbageDue := s.lastGarbage.IsZero() || now.Sub(s.lastGarbage) >= RegistryMaintenanceInterval
	return retentionDue, garbageDue
}

func (s *RegistryService) dueMaintenance(retentionDue, garbageDue bool) (bool, bool) {
	now := time.Now()
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	if retentionDue && !s.lastRetention.IsZero() && now.Sub(s.lastRetention) < RegistryMaintenanceInterval {
		retentionDue = false
	}
	if garbageDue && !s.lastGarbage.IsZero() && now.Sub(s.lastGarbage) < RegistryMaintenanceInterval {
		garbageDue = false
	}
	return retentionDue, garbageDue
}

func maintenanceOperation(options RegistryMaintenanceOptions) string {
	switch {
	case options.Retention && options.GarbageCollection:
		return "retention+gc"
	case options.Retention:
		return "retention"
	case options.GarbageCollection:
		return "gc"
	default:
		return "none"
	}
}

func (s *RegistryService) maintenanceBackend() (registryMaintenanceBackend, error) {
	backend, ok := s.runner.(registryMaintenanceBackend)
	if !ok {
		return nil, errors.New("registry maintenance backend is not configured")
	}
	return backend, nil
}

func (s *RegistryService) retainVersionsLocked(ctx context.Context, defaultKeep int, policies map[string]int, protected map[string]bool) (int, error) {
	storage, ok := s.runner.(RegistryVersionStorage)
	if !ok {
		return 0, errors.New("registry version storage is not configured")
	}
	images, err := s.ListRegistryVersionImages(ctx)
	if err != nil {
		return 0, err
	}
	repositories := make(map[string]bool)
	for _, image := range images {
		repositories[image.Repository] = true
	}
	names := make([]string, 0, len(repositories))
	for name := range repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	type deletionBatch struct {
		repository string
		versions   []application.RegistryVersion
		count      int
	}
	batches := make([]deletionBatch, 0)
	for _, repository := range names {
		versions := application.GroupRegistryVersions(images, repository)
		references := make(map[string][]string, len(versions))
		for _, version := range versions {
			refs, inspectErr := storage.RegistryManifestReferences(ctx, s.container, repository, version.Digest)
			if inspectErr != nil {
				return 0, fmt.Errorf("inspect registry manifest: %w", inspectErr)
			}
			references[version.Digest] = refs
		}
		keep := application.EffectiveRegistryKeep(defaultKeep, policies, repository)
		plan, planErr := application.PlanRegistryRetention(versions, repository, keep, protected, references)
		if planErr != nil {
			return 0, planErr
		}
		if len(plan.DeleteOrder) > 0 {
			batches = append(batches, deletionBatch{
				repository: repository,
				versions:   plan.DeleteOrder,
				count:      plan.PurgedVersions,
			})
		}
	}
	deleted := 0
	for _, batch := range batches {
		for _, version := range batch.versions {
			if err := ctx.Err(); err != nil {
				return deleted, err
			}
			if err := storage.DeleteRegistryVersion(ctx, s.container, batch.repository, version.Digest); err != nil {
				return deleted, fmt.Errorf("delete retained registry version: %w", err)
			}
		}
		deleted += batch.count
	}
	return deleted, nil
}

func (s *RegistryService) verifyRegistryReadOnly(ctx context.Context, backend registryMaintenanceBackend, probeRepository string) error {
	accepted, err := backend.RegistryWritesAccepted(ctx, s.container, probeRepository)
	if err != nil {
		return err
	}
	if accepted {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = backend.RemoveRegistryRepository(cleanupCtx, s.container, probeRepository)
		cancel()
		return errors.New("registry did not enter read-only mode")
	}
	return nil
}

func (s *RegistryService) verifyRegistryWritable(ctx context.Context, backend registryMaintenanceBackend, probeRepository string) error {
	accepted, err := backend.RegistryWritesAccepted(ctx, s.container, probeRepository)
	if err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = backend.RemoveRegistryRepository(cleanupCtx, s.container, probeRepository)
	cancel()
	if !accepted {
		return errors.New("registry did not return to writable mode")
	}
	return nil
}

func (s *RegistryService) restoreRegistryAfterMaintenance(ctx context.Context, backend registryMaintenanceBackend, state registryMaintenanceState, probeRepository string, terminateGarbageCollector bool) error {
	if terminateGarbageCollector {
		if err := backend.RestartRegistryContainer(ctx, s.container); err != nil {
			return fmt.Errorf("stop registry garbage collection: %w", err)
		}
		if err := backend.WaitRegistryReady(ctx, s.container, registryMaintenanceReadyTimeout); err != nil {
			return err
		}
	}
	if err := backend.ApplyRegistryConfig(ctx, s.container, state.ConfigPath, state.OriginalConfig); err != nil {
		return fmt.Errorf("restore registry configuration: %w", err)
	}
	state.Phase = registryMaintenancePhaseRestoring
	if err := s.writeMaintenanceState(state); err != nil {
		return err
	}
	if err := backend.RestartRegistryContainer(ctx, s.container); err != nil {
		return err
	}
	if err := backend.WaitRegistryReady(ctx, s.container, registryMaintenanceReadyTimeout); err != nil {
		return err
	}
	state.Phase = registryMaintenancePhaseRestoreVerify
	if err := s.writeMaintenanceState(state); err != nil {
		return err
	}
	if err := s.verifyRegistryWritable(ctx, backend, probeRepository); err != nil {
		return err
	}
	return s.removeMaintenanceState()
}

func newRegistryMaintenanceProbe() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return registryMaintenanceProbeRepository + "-" + hex.EncodeToString(random[:])
	}
	return registryMaintenanceProbeRepository
}

func normalizeRegistryConfig(contents []byte) string {
	return strings.TrimSpace(string(contents))
}

func ensureRegistryMaintenanceDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create registry maintenance state directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect registry maintenance state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("registry maintenance state path is not a directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect registry maintenance state directory: %w", err)
	}
	return nil
}

func (s *RegistryService) maintenanceStatePath() string {
	return filepath.Join(s.stateDir, registryMaintenanceStateFile)
}

func (s *RegistryService) writeMaintenanceState(state registryMaintenanceState) error {
	if s.stateDir == "" {
		return errors.New("registry maintenance state directory is not configured")
	}
	if err := ensureRegistryMaintenanceDirectory(s.stateDir); err != nil {
		return err
	}
	contents, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode registry maintenance state: %w", err)
	}
	if len(contents) > registryMaintenanceMaxStateSize {
		return errors.New("registry maintenance state is too large")
	}
	if err := writeManagedFile(s.maintenanceStatePath(), string(contents), 0o600); err != nil {
		return fmt.Errorf("write registry maintenance state: %w", err)
	}
	return nil
}

func (s *RegistryService) readMaintenanceState() (registryMaintenanceState, bool, error) {
	if s.stateDir == "" {
		return registryMaintenanceState{}, false, nil
	}
	path := s.maintenanceStatePath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return registryMaintenanceState{}, false, nil
	}
	if err != nil {
		return registryMaintenanceState{}, false, fmt.Errorf("inspect registry maintenance state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return registryMaintenanceState{}, false, errors.New("registry maintenance state is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return registryMaintenanceState{}, false, fmt.Errorf("read registry maintenance state: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, registryMaintenanceMaxStateSize+1))
	if err != nil {
		return registryMaintenanceState{}, false, fmt.Errorf("read registry maintenance state: %w", err)
	}
	if len(contents) > registryMaintenanceMaxStateSize {
		return registryMaintenanceState{}, false, errors.New("registry maintenance state is too large")
	}
	var state registryMaintenanceState
	if err := json.Unmarshal(contents, &state); err != nil {
		return registryMaintenanceState{}, false, fmt.Errorf("decode registry maintenance state: %w", err)
	}
	return state, true, nil
}

func (s *RegistryService) removeMaintenanceState() error {
	if s.stateDir == "" {
		return nil
	}
	err := os.Remove(s.maintenanceStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove registry maintenance state: %w", err)
	}
	if err := syncDirectory(s.stateDir); err != nil {
		return fmt.Errorf("sync registry maintenance state directory: %w", err)
	}
	return nil
}
