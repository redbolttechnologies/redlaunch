package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

const maintenanceTestConfig = `version: 0.1
storage:
    filesystem:
        rootdirectory: /var/lib/registry
http:
    addr: :5000
`

type fakeRegistryMaintenanceBackend struct {
	configPath    string
	config        []byte
	containerID   string
	contents      []compose.RegistryContent
	refs          map[string][]string
	deleted       []string
	restartConfig []string
	gcCalls       int
	gcErr         error
	beforeBytes   int64
	afterBytes    int64
	sizeKnown     bool
	applyErr      error
	restoreErr    error
}

func newFakeRegistryMaintenanceBackend() *fakeRegistryMaintenanceBackend {
	return &fakeRegistryMaintenanceBackend{
		configPath:  "/etc/distribution/config.yml",
		config:      []byte(maintenanceTestConfig),
		containerID: "container-1",
		refs:        map[string][]string{},
		sizeKnown:   true,
		beforeBytes: 10_000,
		afterBytes:  4_000,
	}
}

func (f *fakeRegistryMaintenanceBackend) RegistryContainerTarget(context.Context, string) (compose.RegistryMaintenanceTarget, error) {
	return compose.RegistryMaintenanceTarget{
		ContainerID: f.containerID,
		ConfigPath:  f.configPath,
		Config:      append([]byte(nil), f.config...),
		WasRunning:  true,
	}, nil
}

func (f *fakeRegistryMaintenanceBackend) ApplyRegistryConfig(_ context.Context, _ string, _ string, contents []byte) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.restoreErr != nil && !strings.Contains(string(contents), "readonly:") {
		return f.restoreErr
	}
	f.config = append([]byte(nil), contents...)
	return nil
}

func (f *fakeRegistryMaintenanceBackend) RestartRegistryContainer(context.Context, string) error {
	f.restartConfig = append(f.restartConfig, string(f.config))
	return nil
}

func (f *fakeRegistryMaintenanceBackend) WaitRegistryReady(context.Context, string, time.Duration) error {
	return nil
}

func (f *fakeRegistryMaintenanceBackend) RegistryWritesAccepted(context.Context, string, string) (bool, error) {
	return !strings.Contains(string(f.config), "readonly:"), nil
}

func (f *fakeRegistryMaintenanceBackend) RemoveRegistryRepository(context.Context, string, string) error {
	return nil
}

func (f *fakeRegistryMaintenanceBackend) GarbageCollectRegistry(context.Context, string, string) error {
	f.gcCalls++
	return f.gcErr
}

func (f *fakeRegistryMaintenanceBackend) RegistryStorageBytes(context.Context, string) (int64, bool) {
	if f.gcCalls == 0 {
		return f.beforeBytes, f.sizeKnown
	}
	return f.afterBytes, f.sizeKnown
}

func (f *fakeRegistryMaintenanceBackend) ListRegistryContents(context.Context, string) ([]compose.RegistryContent, error) {
	return append([]compose.RegistryContent(nil), f.contents...), nil
}

func (f *fakeRegistryMaintenanceBackend) RegistryManifestReferences(_ context.Context, _ string, _ string, digest string) ([]string, error) {
	return f.refs[digest], nil
}

func (f *fakeRegistryMaintenanceBackend) DeleteRegistryVersion(_ context.Context, _ string, repository, digest string) error {
	f.deleted = append(f.deleted, repository+"@"+digest)
	remaining := make([]compose.RegistryContent, 0, len(f.contents))
	for _, content := range f.contents {
		if content.Repository != repository || content.Digest != digest {
			remaining = append(remaining, content)
		}
	}
	f.contents = remaining
	return nil
}

type fakeRetentionStore struct {
	keep     int
	policies map[string]int
	err      error
}

func (s *fakeRetentionStore) GetRegistryRetentionKeep(context.Context) (int, error) {
	return s.keep, s.err
}
func (s *fakeRetentionStore) SetRegistryRetentionKeep(_ context.Context, keep int) error {
	if s.err != nil {
		return s.err
	}
	s.keep = keep
	return nil
}
func (s *fakeRetentionStore) ListRegistryRetentionPolicies(context.Context) ([]application.RegistryRetentionPolicy, error) {
	if s.err != nil {
		return nil, s.err
	}
	policies := make([]application.RegistryRetentionPolicy, 0, len(s.policies))
	for repository, keep := range s.policies {
		policies = append(policies, application.RegistryRetentionPolicy{Repository: repository, KeepCount: keep})
	}
	return policies, nil
}
func (s *fakeRetentionStore) SetRegistryRetentionPolicy(_ context.Context, repository string, keep int) error {
	if s.err != nil {
		return s.err
	}
	s.policies[repository] = keep
	return nil
}
func (s *fakeRetentionStore) DeleteRegistryRetentionPolicy(_ context.Context, repository string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.policies, repository)
	return nil
}

func maintenanceTestService(t *testing.T, backend *fakeRegistryMaintenanceBackend, keep int) *RegistryService {
	t.Helper()
	registry, err := NewRegistryService(RegistryContainerName, backend)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetRetentionStore(&fakeRetentionStore{keep: keep, policies: map[string]int{}})
	if err := registry.SetMaintenanceStateDir(filepath.Join(t.TempDir(), "maintenance")); err != nil {
		t.Fatal(err)
	}
	return registry
}

func maintenanceContent(repo, tag, c string, age time.Duration) compose.RegistryContent {
	return compose.RegistryContent{
		Repository:       repo,
		Tag:              tag,
		Digest:           retentionDigest(c),
		ManifestPushedAt: time.Now().Add(-age),
	}
}

func TestApplyMaintenanceRetainsVersionsAndCollectsGarbage(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	backend.contents = []compose.RegistryContent{
		maintenanceContent("app", "v1", "a", time.Hour),
		maintenanceContent("app", "v2", "b", 2*time.Hour),
		maintenanceContent("app", "v3", "c", 3*time.Hour),
	}
	registry := maintenanceTestService(t, backend, 1)

	result, err := registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{
		Retention:         true,
		GarbageCollection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RetentionCompleted || !result.GarbageCollectionCompleted {
		t.Fatalf("result = %#v, want retention and GC completed", result)
	}
	if result.DeletedVersions != 2 {
		t.Fatalf("deleted = %d, want 2", result.DeletedVersions)
	}
	if !result.ReclaimedBytesKnown || result.ReclaimedBytes != 6000 {
		t.Fatalf("reclaimed = (%d, %v), want 6000 known", result.ReclaimedBytes, result.ReclaimedBytesKnown)
	}
	if len(backend.deleted) != 2 {
		t.Fatalf("deleted versions = %#v, want 2", backend.deleted)
	}
	if string(backend.config) != maintenanceTestConfig {
		t.Fatalf("config not restored:\n%s", backend.config)
	}
	if len(backend.restartConfig) != 2 {
		t.Fatalf("restarts = %d, want 2", len(backend.restartConfig))
	}
	if !strings.Contains(backend.restartConfig[0], "readonly:") {
		t.Fatal("first restart was not read-only")
	}
	if strings.Contains(backend.restartConfig[1], "readonly:") {
		t.Fatal("final restart was still read-only")
	}
	if _, err := os.Stat(registry.maintenanceStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance state still exists: %v", err)
	}
	if registry.LastRetention().IsZero() || registry.LastGarbageCollection().IsZero() {
		t.Fatalf("maintenance timestamps were not recorded: retention=%v gc=%v", registry.LastRetention(), registry.LastGarbageCollection())
	}
}

func TestApplyMaintenanceUsesPerRepositoryRetentionOverrides(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	for index, tag := range []string{"v1", "v2", "v3"} {
		backend.contents = append(backend.contents,
			maintenanceContent("app", tag, string(rune('a'+index)), time.Duration(index+1)*time.Hour),
			maintenanceContent("other", tag, string(rune('d'+index)), time.Duration(index+1)*time.Hour),
		)
	}
	registry, err := NewRegistryService(RegistryContainerName, backend)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetRetentionStore(&fakeRetentionStore{keep: 1, policies: map[string]int{"other": 2}})
	if err := registry.SetMaintenanceStateDir(filepath.Join(t.TempDir(), "maintenance")); err != nil {
		t.Fatal(err)
	}
	result, err := registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{Retention: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedVersions != 3 {
		t.Fatalf("deleted = %d, want 3 (2 from app, 1 from other)", result.DeletedVersions)
	}
}

func TestApplyMaintenanceTerminatesGarbageCollectorBeforeRestore(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	backend.gcErr = errors.New("gc interrupted")
	registry := maintenanceTestService(t, backend, 1)

	_, err := registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{GarbageCollection: true})
	if err == nil {
		t.Fatal("expected GC error")
	}
	if string(backend.config) != maintenanceTestConfig {
		t.Fatalf("config not restored:\n%s", backend.config)
	}
	if len(backend.restartConfig) != 3 {
		t.Fatalf("restarts = %d, want 3 (read-only entry, GC termination, writable restore)", len(backend.restartConfig))
	}
	if !strings.Contains(backend.restartConfig[1], "readonly:") {
		t.Fatal("GC termination restart was not read-only")
	}
	if strings.Contains(backend.restartConfig[2], "readonly:") {
		t.Fatal("restore restart was still read-only")
	}
}

func TestApplyMaintenanceKeepsStateWhenRestoreFails(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	backend.restoreErr = errors.New("cannot restore config")
	registry := maintenanceTestService(t, backend, 1)

	if _, err := registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{GarbageCollection: true}); err == nil {
		t.Fatal("expected restore error")
	}
	if _, err := os.Stat(registry.maintenanceStatePath()); err != nil {
		t.Fatalf("maintenance state missing after restore failure: %v", err)
	}
	if registry.MaintenanceError() == nil {
		t.Fatal("maintenance is not blocked after restore failure")
	}
}

func TestApplyMaintenanceScheduledSkipsRecentRuns(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	registry := maintenanceTestService(t, backend, 1)
	now := time.Now()
	registry.scheduleMu.Lock()
	registry.lastRetention = now
	registry.lastGarbage = now
	registry.scheduleMu.Unlock()

	result, err := registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{
		Retention:         true,
		GarbageCollection: true,
		Scheduled:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RetentionCompleted || result.GarbageCollectionCompleted || len(backend.restartConfig) != 0 {
		t.Fatalf("recent maintenance ran again: result=%#v restarts=%d", result, len(backend.restartConfig))
	}
}

func TestRecoverRegistryMaintenanceRestoresReadOnlyConfig(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	readOnly, err := compose.RegistryReadOnlyConfig(backend.config)
	if err != nil {
		t.Fatal(err)
	}
	registry := maintenanceTestService(t, backend, 1)
	state := registryMaintenanceState{
		Version:        registryMaintenanceStateVersion,
		Container:      RegistryContainerName,
		ContainerID:    backend.containerID,
		ConfigPath:     backend.configPath,
		OriginalConfig: append([]byte(nil), backend.config...),
		ReadOnlyConfig: readOnly,
		Operation:      "gc",
		Phase:          registryMaintenancePhaseCollecting,
		StartedAt:      time.Now().UTC(),
	}
	if err := registry.writeMaintenanceState(state); err != nil {
		t.Fatal(err)
	}
	backend.config = readOnly

	if err := registry.RecoverRegistryMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if string(backend.config) != maintenanceTestConfig {
		t.Fatalf("config not restored:\n%s", backend.config)
	}
	if _, err := os.Stat(registry.maintenanceStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance state still exists: %v", err)
	}
	if len(backend.restartConfig) < 2 {
		t.Fatalf("restarts = %d, want at least 2", len(backend.restartConfig))
	}
}

func TestRecoverRegistryMaintenanceClearsStateAfterContainerReplacement(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	readOnly, err := compose.RegistryReadOnlyConfig(backend.config)
	if err != nil {
		t.Fatal(err)
	}
	registry := maintenanceTestService(t, backend, 1)
	state := registryMaintenanceState{
		Version:        registryMaintenanceStateVersion,
		Container:      RegistryContainerName,
		ContainerID:    "old-container",
		ConfigPath:     backend.configPath,
		OriginalConfig: append([]byte(nil), backend.config...),
		ReadOnlyConfig: readOnly,
		Phase:          registryMaintenancePhaseCollecting,
	}
	if err := registry.writeMaintenanceState(state); err != nil {
		t.Fatal(err)
	}
	if err := registry.RecoverRegistryMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registry.maintenanceStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance state still exists: %v", err)
	}
}

func TestRecoverRegistryMaintenanceRefusesChangedContainerConfig(t *testing.T) {
	backend := newFakeRegistryMaintenanceBackend()
	readOnly, err := compose.RegistryReadOnlyConfig(backend.config)
	if err != nil {
		t.Fatal(err)
	}
	registry := maintenanceTestService(t, backend, 1)
	state := registryMaintenanceState{
		Version:        registryMaintenanceStateVersion,
		Container:      RegistryContainerName,
		ContainerID:    backend.containerID,
		ConfigPath:     backend.configPath,
		OriginalConfig: append([]byte(nil), backend.config...),
		ReadOnlyConfig: readOnly,
		Phase:          registryMaintenancePhaseReadOnly,
	}
	if err := registry.writeMaintenanceState(state); err != nil {
		t.Fatal(err)
	}
	backend.config = []byte("operator changed this config\n")

	if err := registry.RecoverRegistryMaintenance(t.Context()); err == nil {
		t.Fatal("changed config was overwritten")
	}
	if _, err := os.Stat(registry.maintenanceStatePath()); err != nil {
		t.Fatalf("maintenance state missing after refusal: %v", err)
	}
}
