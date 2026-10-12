package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/compose"
)

type dashboardDockerFake struct {
	containers []compose.DashboardContainer
	disk       compose.DashboardDisk
	stats      compose.DashboardStats
	calls      int
	statsIDs   chan string
	failed     bool
}

func (f *dashboardDockerFake) DashboardContainers(context.Context) ([]compose.DashboardContainer, error) {
	f.calls++
	if f.failed {
		return nil, errors.New("unavailable")
	}
	return f.containers, nil
}
func (f *dashboardDockerFake) DashboardContainerStats(_ context.Context, id string) (compose.DashboardStats, error) {
	if f.statsIDs != nil {
		f.statsIDs <- id
	}
	return f.stats, nil
}
func (f *dashboardDockerFake) DashboardDiskUsage(context.Context) (compose.DashboardDisk, error) {
	if f.failed {
		return compose.DashboardDisk{}, errors.New("unavailable")
	}
	return f.disk, nil
}
func (f *dashboardDockerFake) PruneUnusedImages(context.Context) (uint64, error) { return 4096, nil }

func TestDashboardContainersAreIndividualManagedServicesCoreFirst(t *testing.T) {
	container := func(id, name, dir, state string) compose.DashboardContainer {
		return compose.DashboardContainer{ID: strings.Repeat(id, 64), Names: []string{"/" + name}, State: state, Status: "Up 2 hours", Labels: map[string]string{"redlaunch.managed": "true", "com.docker.compose.project.working_dir": dir}}
	}
	fake := &dashboardDockerFake{statsIDs: make(chan string, 16), containers: []compose.DashboardContainer{
		container("a", "aaa-app", "/projects/applications/app", "running"), container("b", "redbolt-proxy", "/projects/core/proxy", "running"), container("c", "redbolt-databases", "/projects/core/databases", "exited"), container("d", "redbolt-redlaunch", "/manager", "running"), container("e", "not-core", "/projects/core-other", "running"),
		{Names: []string{"/unmanaged"}, State: "running"},
	}}
	fake.stats.CPUStats.CPUUsage.TotalUsage = 200
	fake.stats.PreCPUStats.CPUUsage.TotalUsage = 100
	fake.stats.CPUStats.SystemCPUUsage = 1000
	fake.stats.PreCPUStats.SystemCPUUsage = 500
	fake.stats.CPUStats.OnlineCPUs = 4
	fake.stats.MemoryStats.Usage = 1024
	fake.stats.MemoryStats.Stats = map[string]uint64{"inactive_file": 256}
	svc := NewDashboardService(fake, "/projects")
	got, err := svc.CollectDockerDashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"redbolt-databases", "redbolt-proxy", "redbolt-redlaunch", "aaa-app", "not-core"}
	if len(got.Containers) != len(want) {
		t.Fatal(got.Containers)
	}
	for i, name := range want {
		c := got.Containers[i]
		if c.Name != name || c.Core != (i < 3) {
			t.Fatal(got.Containers)
		}
		if c.Running && (c.CPUPercent != 80 || c.MemoryBytes != 768) {
			t.Fatal(c)
		}
	}
	if got.Containers[0].Available || len(fake.statsIDs) != 4 {
		t.Fatal("stopped/unmanaged containers must not be sampled")
	}
	if len(got.Trends) != 4 {
		t.Fatal("missing trends")
	}
	_, _ = svc.CollectDockerDashboard(context.Background())
	if fake.calls != 1 {
		t.Fatal("dashboard sample not cached")
	}
	reclaimed, err := svc.PruneUnusedImages(context.Background())
	if err != nil || reclaimed != 4096 {
		t.Fatal(reclaimed, err)
	}
	_, _ = svc.CollectDockerDashboard(context.Background())
	if fake.calls != 2 {
		t.Fatal("pruning did not invalidate disk usage cache")
	}
}
func TestDashboardVolumesAreDeduplicatedAndDoNotInventQuota(t *testing.T) {
	fake := &dashboardDockerFake{}
	var c compose.DashboardContainer
	c.Labels = map[string]string{"redlaunch.managed": "true"}
	c.Mounts = append(c.Mounts, struct{ Type, Name, Destination string }{"volume", "shared", "/data"}, struct{ Type, Name, Destination string }{"volume", "shared", "/other"}, struct{ Type, Name, Destination string }{"bind", "host", "/host"})
	fake.containers = []compose.DashboardContainer{c}
	fake.disk.LayersSize = 1234
	fake.disk.Volumes = append(fake.disk.Volumes, struct {
		Name      string
		UsageData *struct {
			Size     int64
			RefCount int64
		}
	}{"shared", &struct {
		Size     int64
		RefCount int64
	}{1024, 2}}, struct {
		Name      string
		UsageData *struct {
			Size     int64
			RefCount int64
		}
	}{"unmanaged", nil})
	got, err := NewDashboardService(fake, "/projects").CollectDockerDashboard(context.Background())
	if err != nil || len(got.Volumes) != 1 || got.Volumes[0].Bytes != 1024 || got.Volumes[0].MountedAt != "/data, /other" || !got.Volumes[0].Available || got.ImageBytes != 1234 {
		t.Fatal(got, err)
	}
}
func TestDashboardUnavailableAndHistoryBounds(t *testing.T) {
	fake := &dashboardDockerFake{failed: true}
	svc := NewDashboardService(fake, "/projects")
	got, err := svc.CollectDockerDashboard(context.Background())
	if err != nil || !got.ContainersUnavailable || !got.DiskUnavailable {
		t.Fatal(got, err)
	}
	fake.failed = false
	fake.containers = []compose.DashboardContainer{{ID: strings.Repeat("a", 64), Names: []string{"/app"}, State: "running", Labels: map[string]string{"redlaunch.managed": "true"}}}
	svc.at = time.Time{}
	svc.history["app"] = []UsageSample{{At: time.Now().Add(-2 * time.Hour)}, {At: time.Now().Add(-20 * time.Second)}}
	svc.history["removed"] = []UsageSample{{At: time.Now()}}
	got, err = svc.CollectDockerDashboard(context.Background())
	if err != nil || len(got.Trends) != 1 || len(got.Trends[0].Samples) != 2 || len(svc.history) != 1 {
		t.Fatal(got, err)
	}
}
