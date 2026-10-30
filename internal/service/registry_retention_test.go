package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

type fakeRegistryMaintenance struct {
	calls  []RegistryMaintenanceOptions
	result RegistryMaintenanceResult
	err    error
	notify chan struct{}
}

func (f *fakeRegistryMaintenance) ApplyMaintenance(_ context.Context, options RegistryMaintenanceOptions) (RegistryMaintenanceResult, error) {
	f.calls = append(f.calls, options)
	if f.notify != nil {
		f.notify <- struct{}{}
	}
	return f.result, f.err
}

func retentionDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func TestApplicationsRetentionUsesFullProtectionAndManualOptions(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
	runner.runningDigest = retentionDigest("d")
	maintenance := &fakeRegistryMaintenance{result: RegistryMaintenanceResult{DeletedVersions: 2, RetentionCompleted: true}}
	apps.SetRegistryMaintenance(maintenance)

	result, err := apps.RetainRegistryVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedVersions != 2 || len(maintenance.calls) != 1 {
		t.Fatalf("result = %#v, calls = %#v", result, maintenance.calls)
	}
	options := maintenance.calls[0]
	if !options.Retention || options.GarbageCollection || options.Scheduled {
		t.Fatalf("options = %#v, want manual retention only", options)
	}
	for _, key := range []string{
		application.RegistryImageKey("app", "production"),
		application.RegistryImageKey("app", retentionDigest("d")),
	} {
		if !options.Protected[key] {
			t.Fatalf("protected missing %q in %#v", key, options.Protected)
		}
	}
}

func TestApplicationsRetentionProtectsDigestPinnedService(t *testing.T) {
	digest := retentionDigest("c")
	apps, _, _, _ := versionTestService(t, "localhost:5000/app@"+digest)
	maintenance := &fakeRegistryMaintenance{}
	apps.SetRegistryMaintenance(maintenance)
	if _, err := apps.RetainRegistryVersions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(maintenance.calls) != 1 {
		t.Fatalf("calls = %#v", maintenance.calls)
	}
	key := application.RegistryImageKey("app", digest)
	if !maintenance.calls[0].Protected[key] {
		t.Fatalf("digest-pinned service not protected: %#v", maintenance.calls[0].Protected)
	}
}

func TestApplicationsCollectGarbageUsesManualGCOptions(t *testing.T) {
	apps, _, _, _ := versionTestService(t, "nginx:stable")
	maintenance := &fakeRegistryMaintenance{result: RegistryMaintenanceResult{GarbageCollectionCompleted: true, ReclaimedBytes: 1024, ReclaimedBytesKnown: true}}
	apps.SetRegistryMaintenance(maintenance)

	result, err := apps.CollectRegistryGarbage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.GarbageCollectionCompleted || result.ReclaimedBytes != 1024 {
		t.Fatalf("result = %#v", result)
	}
	if len(maintenance.calls) != 1 {
		t.Fatalf("calls = %#v", maintenance.calls)
	}
	options := maintenance.calls[0]
	if options.Retention || !options.GarbageCollection || options.Scheduled {
		t.Fatalf("options = %#v, want manual GC only", options)
	}
}

func TestApplicationsRetentionFailsClosedOnInspectionErrors(t *testing.T) {
	for _, resolverError := range []bool{false, true} {
		apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
		if resolverError {
			runner.resolveErr = errors.New("bad compose")
		} else {
			runner.digestErr = errors.New("docker unavailable")
		}
		maintenance := &fakeRegistryMaintenance{}
		apps.SetRegistryMaintenance(maintenance)
		if _, err := apps.RetainRegistryVersions(t.Context()); err == nil {
			t.Fatal("expected inspection error")
		}
		if len(maintenance.calls) != 0 {
			t.Fatal("maintenance ran with incomplete runtime protection")
		}
	}
}

func TestApplicationsRetentionReleasesLeasesAfterFailure(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
	runner.resolveErr = errors.New("cannot resolve image")
	apps.SetRegistryMaintenance(&fakeRegistryMaintenance{})
	if _, err := apps.RetainRegistryVersions(t.Context()); err == nil {
		t.Fatal("expected inspection error")
	}
	lease, err := apps.tryAcquireApplicationProject(1)
	if err != nil {
		t.Fatalf("retention left project locked: %v", err)
	}
	lease.release()
}

func TestRegistryMaintenanceWorkerRunsAtStartupAndStopsOnCancellation(t *testing.T) {
	apps, _, _, _ := versionTestService(t, "nginx:stable")
	maintenance := &fakeRegistryMaintenance{
		result: RegistryMaintenanceResult{RetentionCompleted: true, GarbageCollectionCompleted: true},
		notify: make(chan struct{}, 1),
	}
	apps.SetRegistryMaintenance(maintenance)
	registry, err := NewRegistryService(RegistryContainerName, &stubRegistryContentLister{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		apps.RunRegistryMaintenanceWorker(ctx, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case <-maintenance.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("startup maintenance did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance worker did not stop")
	}
	if len(maintenance.calls) != 1 {
		t.Fatalf("calls = %#v, want one startup pass", maintenance.calls)
	}
	if !maintenance.calls[0].Scheduled || !maintenance.calls[0].Retention || !maintenance.calls[0].GarbageCollection {
		t.Fatalf("startup options = %#v", maintenance.calls[0])
	}
}

func TestRegistryMaintenanceWorkerSkipsWhenTimestampsAreRecent(t *testing.T) {
	apps, _, _, _ := versionTestService(t, "nginx:stable")
	maintenance := &fakeRegistryMaintenance{notify: make(chan struct{}, 1)}
	apps.SetRegistryMaintenance(maintenance)
	registry, err := NewRegistryService(RegistryContainerName, &stubRegistryContentLister{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	registry.scheduleMu.Lock()
	registry.lastRetention = now
	registry.lastGarbage = now
	registry.scheduleMu.Unlock()
	retentionDue, garbageDue := registry.maintenanceDue(now)
	if retentionDue || garbageDue {
		t.Fatalf("due = (%v, %v), want false, false", retentionDue, garbageDue)
	}
}

func TestRegistryMaintenanceDueAfterInterval(t *testing.T) {
	registry, err := NewRegistryService(RegistryContainerName, &stubRegistryContentLister{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	registry.scheduleMu.Lock()
	registry.lastRetention = now.Add(-RegistryMaintenanceInterval - time.Minute)
	registry.lastGarbage = now.Add(-RegistryMaintenanceInterval - time.Minute)
	registry.scheduleMu.Unlock()
	retentionDue, garbageDue := registry.maintenanceDue(now)
	if !retentionDue || !garbageDue {
		t.Fatalf("due = (%v, %v), want true, true", retentionDue, garbageDue)
	}
}

func TestRegistryMaintenanceBusyReturnsSentinel(t *testing.T) {
	registry, err := NewRegistryService(RegistryContainerName, &stubRegistryContentLister{})
	if err != nil {
		t.Fatal(err)
	}
	requireNoMaintenanceBackend(t, registry)
	registry.mutations.Lock()
	defer registry.mutations.Unlock()
	_, err = registry.ApplyMaintenance(t.Context(), RegistryMaintenanceOptions{Retention: true})
	if !errors.Is(err, application.ErrRegistryBusy) {
		t.Fatalf("ApplyMaintenance() error = %v, want busy", err)
	}
}

func requireNoMaintenanceBackend(t *testing.T, registry *RegistryService) {
	t.Helper()
	if _, err := registry.maintenanceBackend(); err == nil {
		t.Fatal("test runner unexpectedly implements maintenance backend")
	}
}

func TestRetentionPlannerProtectionDoesNotConsumeQuota(t *testing.T) {
	versions := []application.RegistryVersion{
		{Digest: retentionDigest("a"), Tags: []string{"newest"}, PushedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Digest: retentionDigest("b"), Tags: []string{"deployed"}, PushedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{Digest: retentionDigest("c"), Tags: []string{"third"}, PushedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)},
		{Digest: retentionDigest("d"), Tags: []string{"oldest"}, PushedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
	}
	protected := map[string]bool{application.RegistryImageKey("app", "deployed"): true}
	plan, err := application.PlanRegistryRetention(versions, "app", 1, protected, map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, version := range plan.Kept {
		kept = append(kept, version.Digest)
	}
	want := []string{retentionDigest("a"), retentionDigest("b")}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept = %v, want %v", kept, want)
	}
	if len(plan.DeleteOrder) != 2 {
		t.Fatalf("delete order = %#v, want two purged versions", plan.DeleteOrder)
	}
}

func TestRetentionPlannerKeepsUnknownTimesWithoutQuota(t *testing.T) {
	versions := []application.RegistryVersion{
		{Digest: retentionDigest("a"), Tags: []string{"newest"}, PushedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Digest: retentionDigest("b"), Tags: []string{"unknown"}},
		{Digest: retentionDigest("c"), Tags: []string{"oldest"}, PushedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)},
	}
	plan, err := application.PlanRegistryRetention(versions, "app", 1, nil, map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DeleteOrder) != 1 || plan.DeleteOrder[0].Digest != retentionDigest("c") {
		t.Fatalf("delete order = %#v, want only known oldest version", plan.DeleteOrder)
	}
}

func TestRetentionPlannerPreservesIndexChildren(t *testing.T) {
	parent := retentionDigest("a")
	child := retentionDigest("b")
	old := retentionDigest("c")
	versions := []application.RegistryVersion{
		{Digest: parent, Tags: []string{"multi"}, PushedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Digest: child, PushedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Digest: old, Tags: []string{"old"}, PushedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)},
	}
	references := map[string][]string{parent: {child}, child: nil, old: nil}
	plan, err := application.PlanRegistryRetention(versions, "app", 1, nil, references)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DeleteOrder) != 1 || plan.DeleteOrder[0].Digest != old {
		t.Fatalf("delete order = %#v, want only old version", plan.DeleteOrder)
	}
}

func TestRetentionPlannerDeletesParentIndexBeforeChild(t *testing.T) {
	newest := retentionDigest("a")
	parent := retentionDigest("b")
	child := retentionDigest("c")
	versions := []application.RegistryVersion{
		{Digest: newest, Tags: []string{"newest"}, PushedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Digest: parent, Tags: []string{"multi"}, PushedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{Digest: child, PushedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
	}
	references := map[string][]string{newest: nil, parent: {child}, child: nil}
	plan, err := application.PlanRegistryRetention(versions, "app", 1, nil, references)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DeleteOrder) != 2 || plan.DeleteOrder[0].Digest != parent || plan.DeleteOrder[1].Digest != child {
		t.Fatalf("delete order = %#v, want parent then child", plan.DeleteOrder)
	}
	if plan.PurgedVersions != 1 {
		t.Fatalf("purged versions = %d, want one user-facing version", plan.PurgedVersions)
	}
}

func TestRetentionPlannerRejectsIncompleteGraph(t *testing.T) {
	versions := []application.RegistryVersion{{Digest: retentionDigest("a"), Tags: []string{"v1"}}}
	references := map[string][]string{retentionDigest("a"): {retentionDigest("b")}}
	if _, err := application.PlanRegistryRetention(versions, "app", 2, nil, references); err == nil {
		t.Fatal("incomplete graph accepted")
	}
}
