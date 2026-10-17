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
	"redlaunch/internal/compose"
)

type retentionRunner struct {
	stubRegistryContentLister
	refs          map[string][]string
	inspectErr    error
	deleteErr     error
	deleted       []string
	deletedNotify chan struct{}
}

func (r *retentionRunner) RegistryManifestReferences(_ context.Context, _, _, digest string) ([]string, error) {
	return r.refs[digest], r.inspectErr
}
func (r *retentionRunner) DeleteRegistryVersion(_ context.Context, _, repository, digest string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, repository+"@"+digest)
	remaining := make([]compose.RegistryContent, 0, len(r.contents))
	for _, content := range r.contents {
		if content.Repository != repository || content.Digest != digest {
			remaining = append(remaining, content)
		}
	}
	r.contents = remaining
	if r.deletedNotify != nil {
		r.deletedNotify <- struct{}{}
	}
	return nil
}
func retentionDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }
func retentionImage(repo, tag, c string, age int) compose.RegistryContent {
	return compose.RegistryContent{Repository: repo, Tag: tag, Digest: retentionDigest(c), ManifestPushedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC).Add(-time.Duration(age) * time.Hour)}
}
func retentionService(t *testing.T, runner *retentionRunner) *RegistryService {
	t.Helper()
	registry, err := NewRegistryService(RegistryContainerName, runner)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
func TestRetentionKeepsTwoDistinctVersionsPerRepository(t *testing.T) {
	r := &retentionRunner{}
	r.contents = []compose.RegistryContent{
		retentionImage("app", "latest", "a", 0), retentionImage("app", "build1", "a", 0),
		retentionImage("app", "", "b", 1), retentionImage("app", "old", "c", 2),
		retentionImage("other", "", "d", 0), retentionImage("other", "", "e", 1), retentionImage("other", "", "f", 2),
	}
	n, err := retentionService(t, r).RetainVersions(t.Context(), nil)
	want := []string{"app@" + retentionDigest("c"), "other@" + retentionDigest("f")}
	if err != nil || n != 2 || !reflect.DeepEqual(r.deleted, want) {
		t.Fatalf("retention = %d, %v, %v; want %v", n, err, r.deleted, want)
	}
}
func TestRetentionProtectsDeployedTagsDigestsAndUnknownTimes(t *testing.T) {
	r := &retentionRunner{}
	for i, c := range []string{"a", "b", "c", "d", "e", "f"} {
		r.contents = append(r.contents, retentionImage("app", "tag"+c, c, i))
	}
	r.contents[4].ManifestPushedAt = time.Time{}
	protected := map[string]bool{application.RegistryImageKey("app", "tagc"): true, application.RegistryImageKey("app", retentionDigest("d")): true}
	n, err := retentionService(t, r).RetainVersions(t.Context(), protected)
	if err != nil || n != 1 || !reflect.DeepEqual(r.deleted, []string{"app@" + retentionDigest("f")}) {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
	}
}
func TestRetentionPreservesMultiPlatformDependencies(t *testing.T) {
	r := &retentionRunner{refs: map[string][]string{retentionDigest("a"): {retentionDigest("d")}, retentionDigest("b"): {retentionDigest("e")}, retentionDigest("c"): {retentionDigest("f")}}}
	for i, c := range []string{"a", "b", "c", "d", "e", "f"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	n, err := retentionService(t, r).RetainVersions(t.Context(), nil)
	if err != nil || n != 2 || !reflect.DeepEqual(r.deleted, []string{"app@" + retentionDigest("c"), "app@" + retentionDigest("f")}) {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
	}
}
func TestRetentionInspectsEverythingBeforeDeleting(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		r := &retentionRunner{refs: map[string][]string{}}
		for i, c := range []string{"a", "b", "c"} {
			r.contents = append(r.contents, retentionImage("app", "", c, i))
		}
		if incomplete {
			r.refs[retentionDigest("a")] = []string{retentionDigest("d")}
		} else {
			r.inspectErr = errors.New("cannot inspect")
		}
		n, err := retentionService(t, r).RetainVersions(t.Context(), nil)
		if err == nil || n != 0 || len(r.deleted) != 0 {
			t.Fatalf("unsafe cleanup: %d, %v, %v", n, err, r.deleted)
		}
	}
}
func TestApplicationsRetentionProtectsRunningAndPendingVersions(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
	runner.runningDigest = retentionDigest("d")
	r := &retentionRunner{}
	for i, c := range []string{"a", "b", "c", "d", "e"} {
		tag := ""
		if c == "c" {
			tag = "production"
		}
		r.contents = append(r.contents, retentionImage("app", tag, c, i))
	}
	n, err := apps.RetainRegistryVersions(t.Context(), retentionService(t, r))
	if err != nil || n != 1 || !reflect.DeepEqual(r.deleted, []string{"app@" + retentionDigest("e")}) {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
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
		r := &retentionRunner{}
		for i, c := range []string{"a", "b", "c"} {
			r.contents = append(r.contents, retentionImage("app", "", c, i))
		}
		n, err := apps.RetainRegistryVersions(t.Context(), retentionService(t, r))
		if err == nil || n != 0 || len(r.deleted) != 0 {
			t.Fatalf("unsafe cleanup = %d, %v, %v", n, err, r.deleted)
		}
	}
}
func TestApplicationsRetentionProtectsDigestPinnedService(t *testing.T) {
	apps, _, _, _ := versionTestService(t, "localhost:5000/app@"+retentionDigest("c"))
	r := &retentionRunner{}
	for i, c := range []string{"a", "b", "c", "d"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	n, err := apps.RetainRegistryVersions(t.Context(), retentionService(t, r))
	if err != nil || n != 1 || !reflect.DeepEqual(r.deleted, []string{"app@" + retentionDigest("d")}) {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
	}
}

func TestRetentionDeletesObsoleteIndexBeforeNewerChild(t *testing.T) {
	r := &retentionRunner{refs: map[string][]string{retentionDigest("c"): {retentionDigest("d")}}}
	r.contents = []compose.RegistryContent{
		retentionImage("app", "v1", "a", 0), retentionImage("app", "v2", "b", 1),
		retentionImage("app", "", "d", 2), retentionImage("app", "v0", "c", 3),
	}
	n, err := retentionService(t, r).RetainVersions(t.Context(), nil)
	want := []string{"app@" + retentionDigest("c"), "app@" + retentionDigest("d")}
	if err != nil || n != 2 || !reflect.DeepEqual(r.deleted, want) {
		t.Fatalf("retention = %d, %v, %v; want %v", n, err, r.deleted, want)
	}
}

func TestRetentionStopsAfterDeleteFailure(t *testing.T) {
	r := &retentionRunner{deleteErr: errors.New("storage unavailable")}
	for i, c := range []string{"a", "b", "c", "d"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	n, err := retentionService(t, r).RetainVersions(t.Context(), nil)
	if err == nil || n != 0 || len(r.deleted) != 0 {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
	}
}

func TestApplicationsRetentionProtectsRuntimeImageAfterConfigurationChanges(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "nginx:stable")
	runner.runningDigest = retentionDigest("c")
	runner.runtime[0].Image = "localhost:5000/app:production"
	r := &retentionRunner{}
	for i, c := range []string{"a", "b", "c", "d"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	n, err := apps.RetainRegistryVersions(t.Context(), retentionService(t, r))
	if err != nil || n != 1 || !reflect.DeepEqual(r.deleted, []string{"app@" + retentionDigest("d")}) {
		t.Fatalf("retention = %d, %v, %v", n, err, r.deleted)
	}
}

func TestApplicationsRetentionReleasesLeasesAfterFailure(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
	runner.resolveErr = errors.New("cannot resolve image")
	r := &retentionRunner{}
	if _, err := apps.RetainRegistryVersions(t.Context(), retentionService(t, r)); err == nil {
		t.Fatal("expected inspection error")
	}
	lease, err := apps.tryAcquireApplicationProject(1)
	if err != nil {
		t.Fatalf("retention left project locked: %v", err)
	}
	lease.release()
}

func TestRegistryRetentionWorkerRunsAtStartupAndStopsOnCancellation(t *testing.T) {
	apps, _, runner, _ := versionTestService(t, "localhost:5000/app:production")
	runner.runningDigest = retentionDigest("a")
	r := &retentionRunner{deletedNotify: make(chan struct{}, 1)}
	for i, c := range []string{"a", "b", "c"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	registry := retentionService(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		apps.RunRegistryRetention(ctx, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case <-r.deletedNotify:
	case <-time.After(5 * time.Second):
		t.Fatal("startup cleanup did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retention worker did not stop")
	}
	if len(r.deleted) != 1 {
		t.Fatalf("deleted = %v, want one startup pass", r.deleted)
	}
	lease, err := apps.tryAcquireApplicationProject(1)
	if err != nil {
		t.Fatalf("retention left project locked: %v", err)
	}
	lease.release()
}

func TestRegistryRetentionIsIdempotent(t *testing.T) {
	r := &retentionRunner{}
	for i, c := range []string{"a", "b", "c"} {
		r.contents = append(r.contents, retentionImage("app", "", c, i))
	}
	registry := retentionService(t, r)
	if n, err := registry.RetainVersions(t.Context(), nil); err != nil || n != 1 {
		t.Fatalf("first pass = %d, %v", n, err)
	}
	if n, err := registry.RetainVersions(t.Context(), nil); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
	if len(r.contents) != 2 {
		t.Fatalf("retained %d versions, want 2", len(r.contents))
	}
}
