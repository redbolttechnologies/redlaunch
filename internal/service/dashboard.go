package service

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
	"redlaunch/internal/compose"
)

type DashboardDocker interface {
	DashboardContainers(context.Context) ([]compose.DashboardContainer, error)
	DashboardContainerStats(context.Context, string) (compose.DashboardStats, error)
	DashboardDiskUsage(context.Context) (compose.DashboardDisk, error)
	PruneUnusedImages(context.Context) (uint64, error)
}
type DashboardApplicationCatalog interface {
	List(context.Context) ([]application.Application, error)
}

type ContainerUsage struct {
	ApplicationName                       string
	Name, Status                          string
	Core, Running, Available              bool
	CPUPercent                            float64
	MemoryBytes, ReceivedBytes, SentBytes uint64
	Uptime                                string
}
type VolumeUsage struct {
	ApplicationNames []string
	Name, MountedAt  string
	Bytes            uint64
	Available        bool
}
type UsageSample struct {
	At     time.Time
	CPU    float64
	Memory uint64
}
type ServiceTrend struct {
	Name    string
	Samples []UsageSample
}
type DockerDashboard struct {
	Trends                                 []ServiceTrend
	Containers                             []ContainerUsage
	Volumes                                []VolumeUsage
	ImageCount                             int
	ImageBytes                             uint64
	ContainersUnavailable, DiskUnavailable bool
	VolumesUnavailable                     bool
	ApplicationsUnavailable                bool
}
type DashboardService struct {
	docker           DashboardDocker
	coreRoot         string
	applicationsRoot string
	catalog          DashboardApplicationCatalog
	mu               sync.Mutex
	cached           DockerDashboard
	at               time.Time
	history          map[string][]UsageSample
}

func NewDashboardService(docker DashboardDocker, projectsRoot string, catalog DashboardApplicationCatalog) *DashboardService {
	if absolute, err := filepath.Abs(projectsRoot); err == nil {
		projectsRoot = absolute
	}
	return &DashboardService{docker: docker, coreRoot: filepath.Join(projectsRoot, "core"), applicationsRoot: filepath.Join(projectsRoot, "applications"), catalog: catalog, history: map[string][]UsageSample{}}
}
func (s *DashboardService) CollectDockerDashboard(ctx context.Context) (DockerDashboard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < 5*time.Second {
		return s.cached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var result DockerDashboard
	projectNames := map[string]string{}
	directoryNames := map[string]string{}
	if s.catalog != nil {
		apps, err := s.catalog.List(ctx)
		result.ApplicationsUnavailable = err != nil
		if err == nil {
			for _, app := range apps {
				folder, err := application.ValidateFolderName(app.FolderName)
				if err != nil {
					continue
				}
				directory := filepath.Join(s.applicationsRoot, folder)
				projectNames[compose.ProjectName(directory)] = app.Name
				directoryNames[directory] = app.Name
			}
		}
	}
	containers, err := s.docker.DashboardContainers(ctx)
	result.ContainersUnavailable = err != nil
	mounts := map[string]map[string]bool{}
	volumeOwners := map[string]map[string]bool{}
	ids := []string{}
	// Bound concurrent stats requests: Docker samples each running container over ~1s.
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, c := range containers {
		if c.Labels["redlaunch.managed"] != "true" {
			continue
		}
		item := ContainerUsage{Status: c.State, Running: c.State == "running", Uptime: c.Status}
		if len(c.Names) > 0 {
			item.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		directory := filepath.Clean(c.Labels["com.docker.compose.project.working_dir"])
		item.Core = directory == s.coreRoot || strings.HasPrefix(directory, s.coreRoot+string(filepath.Separator)) || item.Name == "redbolt-redlaunch" || item.Name == compose.ProxyContainerName || item.Name == RegistryContainerName || item.Name == managedDatabaseContainerName || c.Labels["com.docker.compose.service"] == "redlaunch"

		item.ApplicationName = projectNames[c.Labels["com.docker.compose.project"]]
		if item.ApplicationName == "" {
			item.ApplicationName = directoryNames[directory]
		}
		if item.Core {
			item.ApplicationName = "Core"
		} else if item.ApplicationName == "" {
			item.ApplicationName = "Unassigned"
			if result.ApplicationsUnavailable {
				item.ApplicationName = "Unavailable"
			}
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" {
				if mounts[m.Name] == nil {
					mounts[m.Name] = map[string]bool{}
				}
				mounts[m.Name][m.Destination] = true
				if volumeOwners[m.Name] == nil {
					volumeOwners[m.Name] = map[string]bool{}
				}
				volumeOwners[m.Name][item.ApplicationName] = true
			}
		}
		result.Containers = append(result.Containers, item)
		ids = append(ids, c.ID)
	}
	for i, c := range result.Containers {
		if !c.Running {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			stats, err := s.docker.DashboardContainerStats(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			item := &result.Containers[i]
			item.Available = true
			cpu, prev := stats.CPUStats, stats.PreCPUStats
			if cpu.CPUUsage.TotalUsage >= prev.CPUUsage.TotalUsage && cpu.SystemCPUUsage > prev.SystemCPUUsage {
				item.CPUPercent = float64(cpu.CPUUsage.TotalUsage-prev.CPUUsage.TotalUsage) / float64(cpu.SystemCPUUsage-prev.SystemCPUUsage) * float64(cpu.OnlineCPUs) * 100
			}
			item.MemoryBytes = stats.MemoryStats.Usage
			cache := stats.MemoryStats.Stats["inactive_file"]
			if cache == 0 {
				cache = stats.MemoryStats.Stats["total_inactive_file"]
			}
			if cache < item.MemoryBytes {
				item.MemoryBytes -= cache
			}
			for _, n := range stats.Networks {
				item.ReceivedBytes += n.RxBytes
				item.SentBytes += n.TxBytes
			}
		}(i, ids[i])
	}
	disk, err := s.docker.DashboardDiskUsage(ctx)
	result.DiskUnavailable = err != nil
	result.VolumesUnavailable = result.DiskUnavailable || result.ContainersUnavailable
	if err == nil {
		result.ImageCount = len(disk.Images)
		result.ImageBytes = disk.LayersSize
		for _, v := range disk.Volumes {
			destinations, managed := mounts[v.Name]
			if !managed {
				continue
			}
			paths := make([]string, 0, len(destinations))
			for p := range destinations {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			item := VolumeUsage{Name: v.Name, MountedAt: strings.Join(paths, ", ")}
			for owner := range volumeOwners[v.Name] {
				item.ApplicationNames = append(item.ApplicationNames, owner)
			}
			sort.Strings(item.ApplicationNames)
			if v.UsageData != nil && v.UsageData.Size >= 0 {
				item.Available = true
				item.Bytes = uint64(v.UsageData.Size)
			}
			result.Volumes = append(result.Volumes, item)
		}
	}
	wg.Wait()
	sort.Slice(result.Containers, func(i, j int) bool {
		a, b := result.Containers[i], result.Containers[j]
		if a.Core != b.Core {
			return a.Core
		}
		return a.Name < b.Name
	})
	sort.Slice(result.Volumes, func(i, j int) bool { return result.Volumes[i].Name < result.Volumes[j].Name })
	now := time.Now()
	live := map[string]bool{}
	for _, c := range result.Containers {
		if !c.Available {
			continue
		}
		live[c.Name] = true
		samples := s.history[c.Name]
		cut := 0
		for cut < len(samples) && samples[cut].At.Before(now.Add(-time.Hour)) {
			cut++
		}
		samples = append([]UsageSample(nil), samples[cut:]...)
		if len(samples) == 0 || now.Sub(samples[len(samples)-1].At) >= 10*time.Second {
			samples = append(samples, UsageSample{At: now, CPU: c.CPUPercent, Memory: c.MemoryBytes})
		}
		if len(samples) > 360 {
			samples = samples[len(samples)-360:]
		}
		s.history[c.Name] = samples
		result.Trends = append(result.Trends, ServiceTrend{Name: c.Name, Samples: append([]UsageSample(nil), samples...)})
	}
	for name := range s.history {
		if !live[name] {
			delete(s.history, name)
		}
	}
	s.cached = result
	s.at = now
	return result, nil
}
func (s *DashboardService) PruneUnusedImages(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	reclaimed, err := s.docker.PruneUnusedImages(ctx)
	s.at = time.Time{}
	return reclaimed, err
}
