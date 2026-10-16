package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"redlaunch/internal/application"
)

type deletionVolumeRunner struct {
	*serviceRuntimeRunner
	removed [][]string
	fail    bool
}

func (r *deletionVolumeRunner) ListServiceDeletionVolumes(context.Context, string, string) ([]application.ServiceDeletionVolume, error) {
	return []application.ServiceDeletionVolume{{Name: "data"}, {Name: "cache"}}, nil
}

func (r *deletionVolumeRunner) RemoveServiceVolumes(_ context.Context, _, _ string, names []string) error {
	r.removed = append(r.removed, append([]string(nil), names...))
	if r.fail {
		return errors.New("volume is in use")
	}
	return nil
}

func TestServiceDeletionVolumeSelectionAndRecovery(t *testing.T) {
	ctx := t.Context()
	database, applications, runtime, _, item, _ := newServiceDeletionTestSetup(t, ctx)
	runner := &deletionVolumeRunner{serviceRuntimeRunner: runtime, fail: true}
	applications.runner = runner
	if err := applications.DeleteServiceWithVolumes(ctx, item.ID, "db", []string{"data"}, nil); err == nil {
		t.Fatal("expected volume removal failure")
	}
	intent, err := database.GetServiceDeletion(ctx, item.ID, "db")
	if err != nil || intent.Stage != "volumes" || !reflect.DeepEqual(intent.Volumes, []string{"data"}) {
		t.Fatalf("intent = %#v, %v", intent, err)
	}
	preview, err := applications.ListServiceDeletionVolumes(ctx, item.ID, "db")
	if err != nil || !reflect.DeepEqual(preview, []application.ServiceDeletionVolume{{Name: "data", Selected: true, Frozen: true}}) {
		t.Fatalf("retry preview = %#v, %v", preview, err)
	}
	runner.fail = false
	// A retry through an older API still honors the saved destructive choices.
	if err := applications.DeleteService(ctx, item.ID, "db"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.removed, [][]string{{"data"}, {"data"}}) {
		t.Fatalf("removed = %v", runner.removed)
	}
	intent, err = database.GetServiceDeletion(ctx, item.ID, "db")
	if err != nil || intent.State != "complete" {
		t.Fatalf("intent = %#v, %v", intent, err)
	}
}

func TestServiceDeletionRetainsVolumesByDefault(t *testing.T) {
	ctx := t.Context()
	_, applications, runtime, _, item, _ := newServiceDeletionTestSetup(t, ctx)
	runner := &deletionVolumeRunner{serviceRuntimeRunner: runtime}
	applications.runner = runner
	if err := applications.DeleteService(ctx, item.ID, "db"); err != nil {
		t.Fatal(err)
	}
	if len(runner.removed) != 0 {
		t.Fatalf("unexpected removal: %v", runner.removed)
	}
}

func TestServiceDeletionRejectsForgedOrDuplicateVolumesBeforeStopping(t *testing.T) {
	for _, names := range [][]string{{"forged"}, {"data", "data"}} {
		ctx := t.Context()
		database, applications, runtime, scheduler, item, _ := newServiceDeletionTestSetup(t, ctx)
		runner := &deletionVolumeRunner{serviceRuntimeRunner: runtime}
		applications.runner = runner
		if err := applications.DeleteServiceWithVolumes(ctx, item.ID, "db", names, nil); err == nil {
			t.Fatalf("accepted %v", names)
		}
		if len(scheduler.disabled) != 0 || len(runner.removed) != 0 {
			t.Fatal("destructive workflow started")
		}
		if _, err := database.GetServiceDeletion(ctx, item.ID, "db"); !errors.Is(err, application.ErrServiceNotFound) {
			t.Fatalf("intent created: %v", err)
		}
	}
}
