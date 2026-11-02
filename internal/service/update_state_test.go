package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

func TestUpdateStateStoreAcquiresAndReleasesOperation(t *testing.T) {
	root := t.TempDir()
	store, err := newUpdateStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:        updateStateSchema,
		OperationID:   "operation-1",
		Owner:         "manager",
		Type:          updateOperationType,
		Phase:         application.UpdatePhasePreparing,
		Version:       "v1.2.3",
		Commit:        "abc123",
		TargetImageID: "sha256:" + "a",
		RuntimeImage:  "redlaunch:local",
		HelperName:    updateHelperName,
		StartedAt:     time.Now().UTC(),
	}
	if err := store.acquire(state.OperationID, state.Owner); err != nil {
		t.Fatal(err)
	}
	if err := store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.readState()
	if err != nil || !ok || got.OperationID != state.OperationID {
		t.Fatalf("readState() = %#v, %t, %v", got, ok, err)
	}
	if err := store.acquire("operation-2", "manager"); !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("second acquire error = %v, want busy", err)
	}
	if err := store.release("operation-2"); err == nil {
		t.Fatal("release by another operation succeeded")
	}
	if err := store.release(state.OperationID); err != nil {
		t.Fatal(err)
	}
	if store.operationRunning() {
		t.Fatal("lock still reported running")
	}
}

func TestUpdateStateStoreRejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "core", "updater")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if _, err := newUpdateStateStore(root); err == nil {
		t.Fatal("symlinked updater directory accepted")
	}
}

func TestUpdateStateStoreCreatesPrivateDirectories(t *testing.T) {
	root := t.TempDir()
	store, err := newUpdateStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.root, store.downloads} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, %v; want private directory", path, info.Mode().Perm(), err)
		}
	}
}

func TestUpdateServiceCheckUsesCachedRelease(t *testing.T) {
	source := &fakeReleaseSource{release: application.ReleaseInfo{
		Version: "v1.2.3",
		Manifest: application.ReleaseManifest{
			Version:            "v1.2.3",
			Commit:             "abc123",
			DeploymentContract: 1,
			Images: map[string]application.ReleaseImage{
				"amd64": {
					Arch:    "amd64",
					Asset:   "redlaunch-image-linux-amd64.tar.gz",
					SHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					ImageID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Tag:     "redlaunch:v1.2.3",
				},
			},
		},
	}}
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:  t.TempDir(),
		Directory:     t.TempDir(),
		ReleaseSource: source,
		Arch:          "amd64",
		Version:       "v1.0.0",
		Docker:        &noopUpdateDocker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := updater.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.LatestVersion != "v1.2.3" {
		t.Fatalf("status = %#v, want update available", status)
	}
	if _, err := updater.CheckForUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 {
		t.Fatalf("release source calls = %d, want cached single call", source.calls)
	}
}

type fakeReleaseSource struct {
	calls   int
	release application.ReleaseInfo
	err     error
}

func (f *fakeReleaseSource) Latest(context.Context) (application.ReleaseInfo, error) {
	f.calls++
	return f.release, f.err
}

func (f *fakeReleaseSource) Download(context.Context, application.ReleaseAsset, string, string, int64) error {
	return nil
}

type noopUpdateDocker struct{}

func (d *noopUpdateDocker) LoadImage(context.Context, string) error { return nil }
func (d *noopUpdateDocker) InspectImage(context.Context, string) (compose.UpdateImageInfo, error) {
	return compose.UpdateImageInfo{}, nil
}
func (d *noopUpdateDocker) TagImage(context.Context, string, string) error        { return nil }
func (d *noopUpdateDocker) ContainerExists(context.Context, string) (bool, error) { return false, nil }
func (d *noopUpdateDocker) RemoveContainer(context.Context, string) error         { return nil }
func (d *noopUpdateDocker) InspectContainer(context.Context, string) (compose.UpdateContainerInfo, error) {
	return compose.UpdateContainerInfo{}, nil
}
func (d *noopUpdateDocker) ComposeConfig(context.Context, string, string, string, string) ([]byte, error) {
	return nil, nil
}
func (d *noopUpdateDocker) RecreateApp(context.Context, string, string, string, string) error {
	return nil
}
func (d *noopUpdateDocker) StartUpdateHelper(context.Context, string, string) error { return nil }
func (d *noopUpdateDocker) ExecHealth(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func TestUpdateStateRejectsStaleRevision(t *testing.T) {
	store, err := newUpdateStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := application.UpdateState{
		Schema:      updateStateSchema,
		Revision:    1,
		OperationID: "revision-operation",
		Owner:       "helper",
		Type:        updateOperationType,
		Phase:       application.UpdatePhaseRestartQueued,
	}
	if err := store.writeState(&initial); err != nil {
		t.Fatal(err)
	}
	stale, ok, err := store.readState()
	if err != nil || !ok {
		t.Fatal(err)
	}
	advanced := stale
	advanced.Phase = application.UpdatePhaseVerifying
	if err := store.writeState(&advanced); err != nil {
		t.Fatal(err)
	}
	stale.Phase = application.UpdatePhaseUncertain
	stale.Owner = "manager"
	if err := store.writeState(&stale); err == nil {
		t.Fatal("stale revision overwrote newer operation state")
	}
	current, ok, err := store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseVerifying {
		t.Fatalf("state after stale write = %#v, %t, %v", current, ok, err)
	}
}

func TestUpdateServiceAdmissionBlocksMutationsUntilReconciled(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:   t.TempDir(),
		Directory:      t.TempDir(),
		Docker:         &noopUpdateDocker{},
		DeferMutations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := updater.allowMutations(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mutation admission error = %v, want deadline", err)
	}
	updater.markAdmitted()
	if err := updater.allowMutations(t.Context()); err != nil {
		t.Fatalf("mutation admission after reconciliation = %v", err)
	}
}

func TestReconcileInterruptedUpdateResolvesDeadHelper(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:   t.TempDir(),
		Directory:      t.TempDir(),
		Docker:         &noopUpdateDocker{},
		DeferMutations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:          updateStateSchema,
		Revision:        1,
		OperationID:     "dead-helper",
		Owner:           "helper",
		Type:            updateOperationType,
		Phase:           application.UpdatePhaseRestartQueued,
		Version:         "v1.2.3",
		Commit:          "abc123",
		TargetImageID:   "sha256:target",
		PreviousImageID: "sha256:previous",
		RuntimeImage:    "redlaunch:local",
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
		t.Fatal(err)
	}
	if err := updater.ReconcileInterruptedUpdate(t.Context()); !errors.Is(err, application.ErrUpdateUncertain) {
		t.Fatalf("dead-helper reconciliation = %v, want uncertain", err)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseUncertain {
		t.Fatalf("state = %#v, %t, %v", current, ok, err)
	}
	if err := updater.allowMutations(t.Context()); err != nil {
		t.Fatalf("resolved uncertain operation did not admit mutations: %v", err)
	}
}

func TestReconcileRejectsMismatchedPersistedIdentity(t *testing.T) {
	projectsRoot := t.TempDir()
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:     projectsRoot,
		Directory:        t.TempDir(),
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "redbolt-redlaunch",
		Docker:           &noopUpdateDocker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:           updateStateSchema,
		Revision:         1,
		OperationID:      "foreign-deployment",
		Owner:            "helper",
		Type:             updateOperationType,
		Phase:            application.UpdatePhaseRestartQueued,
		Version:          "v1.2.3",
		Commit:           "abc123",
		TargetImageID:    "sha256:target",
		PreviousImageID:  "sha256:previous",
		RuntimeImage:     "redlaunch:local",
		ManagerContainer: "other-manager",
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
		t.Fatal(err)
	}
	if err := updater.ReconcileUpdateState(t.Context()); !errors.Is(err, application.ErrUpdateUncertain) {
		t.Fatalf("mismatched identity reconciliation = %v, want uncertain", err)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseUncertain {
		t.Fatalf("state = %#v, %t, %v", current, ok, err)
	}
}

type failingContainerDocker struct {
	noopUpdateDocker
}

func (d *failingContainerDocker) ContainerExists(context.Context, string) (bool, error) {
	return false, errors.New("Docker inspection unavailable")
}

func TestUnresolvedReconciliationKeepsAdmissionClosed(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:   t.TempDir(),
		Directory:      t.TempDir(),
		Docker:         &failingContainerDocker{},
		DeferMutations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:          updateStateSchema,
		Revision:        1,
		OperationID:     "unresolved",
		Owner:           "helper",
		Type:            updateOperationType,
		Phase:           application.UpdatePhaseVerifying,
		Version:         "v1.2.3",
		Commit:          "abc123",
		TargetImageID:   "sha256:target",
		PreviousImageID: "sha256:previous",
		RuntimeImage:    "redlaunch:local",
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.acquire(state.OperationID, "helper"); err != nil {
		t.Fatal(err)
	}
	if err := updater.ReconcileInterruptedUpdate(t.Context()); err == nil {
		t.Fatal("unresolved reconciliation succeeded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := updater.allowMutations(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unresolved reconciliation opened admission: %v", err)
	}
}

func TestUpdateStateConcurrentWritersCommitOnce(t *testing.T) {
	store, err := newUpdateStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := application.UpdateState{
		Schema:      updateStateSchema,
		Revision:    1,
		OperationID: "concurrent-operation",
		Owner:       "helper",
		Type:        updateOperationType,
		Phase:       application.UpdatePhaseRestartQueued,
	}
	if err := store.writeState(&initial); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	var committed atomic.Int32
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			state := initial
			state.Phase = application.UpdatePhaseVerifying
			<-start
			if err := store.writeState(&state); err == nil {
				committed.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if got := committed.Load(); got != 1 {
		t.Fatalf("concurrent writes committed %d times, want exactly one", got)
	}
}

func TestUpdateStateCrossProcessWriter(t *testing.T) {
	root := os.Getenv("REDLAUNCH_UPDATE_STATE_HELPER_ROOT")
	if root == "" {
		t.Skip("helper process only")
	}
	store, err := newUpdateStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:      updateStateSchema,
		Revision:    1,
		OperationID: "cross-process-operation",
		Owner:       "helper",
		Type:        updateOperationType,
		Phase:       application.UpdatePhaseRestartQueued,
	}
	if err := store.writeState(&state); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestUpdateStateCrossProcessCAS(t *testing.T) {
	store, err := newUpdateStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := application.UpdateState{
		Schema:      updateStateSchema,
		Revision:    1,
		OperationID: "cross-process-operation",
		Owner:       "helper",
		Type:        updateOperationType,
		Phase:       application.UpdatePhaseRestartQueued,
	}
	if err := store.writeState(&initial); err != nil {
		t.Fatal(err)
	}
	const writers = 6
	codes := make(chan int, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestUpdateStateCrossProcessWriter$", "-test.count=1")
			command.Env = append(os.Environ(), "REDLAUNCH_UPDATE_STATE_HELPER_ROOT="+store.projectsRoot)
			if err := command.Run(); err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					codes <- exit.ExitCode()
					return
				}
				codes <- -1
				return
			}
			codes <- 0
		}()
	}
	wait.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		switch code {
		case 0:
			successes++
		case 3:
		default:
			t.Fatalf("state writer exited with %d", code)
		}
	}
	if successes != 1 {
		t.Fatalf("cross-process writes committed %d times, want exactly one", successes)
	}
}

func TestReconcileUpdateStateKeepsOtherOperationLocks(t *testing.T) {
	for _, owner := range []string{"prune", "management-access"} {
		t.Run(owner, func(t *testing.T) {
			updater, err := NewUpdateService(UpdateOptions{
				ProjectsRoot:   t.TempDir(),
				Directory:      t.TempDir(),
				Docker:         &noopUpdateDocker{},
				DeferMutations: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := updater.store.acquire("other-operation", owner); err != nil {
				t.Fatal(err)
			}
			if err := updater.ReconcileUpdateState(t.Context()); err != nil {
				t.Fatal(err)
			}
			operationID, gotOwner, ok, err := updater.store.readLock()
			if err != nil || !ok || operationID != "other-operation" || gotOwner != owner {
				t.Fatalf("reconciliation removed %s lock: %q %q %t %v", owner, operationID, gotOwner, ok, err)
			}
		})
	}
}

func TestUpdateReconciliationWithDefaultAdmissionIsIdempotent(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot: t.TempDir(),
		Directory:    t.TempDir(),
		Docker:       &noopUpdateDocker{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := updater.ReconcileUpdateState(t.Context()); err != nil {
			t.Fatal(err)
		}
		updater.markAdmitted()
	}
}

func TestReconcileUpdateStateRequiresItsOwnRecoveryLock(t *testing.T) {
	updater, err := NewUpdateService(UpdateOptions{
		ProjectsRoot:   t.TempDir(),
		Directory:      t.TempDir(),
		Docker:         &noopUpdateDocker{},
		DeferMutations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := application.UpdateState{
		Schema:        updateStateSchema,
		OperationID:   "completed-release",
		Owner:         "helper",
		Type:          updateOperationType,
		Phase:         application.UpdatePhaseComplete,
		TargetImageID: "sha256:target",
		Version:       "v1.2.3",
	}
	if err := updater.store.writeState(&state); err != nil {
		t.Fatal(err)
	}
	if err := updater.store.acquire("active-prune", "prune"); err != nil {
		t.Fatal(err)
	}
	if err := updater.ReconcileUpdateState(t.Context()); !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("reconciliation under a prune lock error = %v, want busy", err)
	}
	current, ok, err := updater.store.readState()
	if err != nil || !ok || current.Phase != application.UpdatePhaseComplete {
		t.Fatalf("state changed without owning recovery exclusion: %#v, %t, %v", current, ok, err)
	}
	operationID, owner, ok, err := updater.store.readLock()
	if err != nil || !ok || operationID != "active-prune" || owner != "prune" {
		t.Fatalf("prune lock changed: %q %q %t %v", operationID, owner, ok, err)
	}
}

func TestEnsureUpdateLockOwnsOrAcquiresExclusion(t *testing.T) {
	store, err := newUpdateStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureUpdateLock("release-operation", "manager"); err != nil {
		t.Fatal(err)
	}
	if operationID, owner, ok, err := store.readLock(); err != nil || !ok || operationID != "release-operation" || owner != "manager" {
		t.Fatalf("acquired lock = %q %q %t %v", operationID, owner, ok, err)
	}
	if err := store.ensureUpdateLock("other-operation", "manager"); !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("foreign lock error = %v, want busy", err)
	}
	if err := store.release("release-operation"); err != nil {
		t.Fatal(err)
	}
	if err := store.acquire("active-prune", "prune"); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureUpdateLock("release-operation", "manager"); !errors.Is(err, application.ErrUpdateBusy) {
		t.Fatalf("prune lock treated as recovery exclusion: %v", err)
	}
}
