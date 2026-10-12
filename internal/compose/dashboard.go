package compose

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
)

// DashboardClient accesses only the local Docker daemon, never a user-supplied URL.
// The socket is an infrastructure detail and is not exposed through HTTP.
type DashboardClient struct{ client *http.Client }

func NewDashboardClient() *DashboardClient {
	return &DashboardClient{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		},
	}}}
}

type DashboardContainer struct {
	ID            string `json:"Id"`
	Names         []string
	Labels        map[string]string
	State, Status string
	Mounts        []struct{ Type, Name, Destination string }
}
type DashboardCPU struct {
	CPUUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs     uint64 `json:"online_cpus"`
}
type DashboardStats struct {
	CPUStats    DashboardCPU `json:"cpu_stats"`
	PreCPUStats DashboardCPU `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64
		Stats map[string]uint64
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	}
}
type DashboardDisk struct {
	LayersSize uint64
	Images     []struct {
		ID string `json:"Id"`
	}
	Volumes []struct {
		Name      string
		UsageData *struct {
			Size     int64
			RefCount int64
		}
	}
}

func (d *DashboardClient) request(ctx context.Context, method, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return errors.New("create Docker dashboard request")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return errors.New("Docker dashboard unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("Docker dashboard request failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStructuredOutput+1))
	if err != nil || len(data) > maxStructuredOutput {
		return errors.New("Docker dashboard response unavailable")
	}
	if json.Unmarshal(data, target) != nil {
		return errors.New("invalid Docker dashboard response")
	}
	return nil
}
func (d *DashboardClient) DashboardContainers(ctx context.Context) ([]DashboardContainer, error) {
	var result []DashboardContainer
	filters := url.QueryEscape(`{"label":["redlaunch.managed=true"]}`)
	err := d.request(ctx, http.MethodGet, "/containers/json?all=1&filters="+filters, &result)
	return result, err
}
func (d *DashboardClient) DashboardContainerStats(ctx context.Context, id string) (DashboardStats, error) {
	var result DashboardStats
	// Container IDs originate in Docker, but still validate before constructing a path.
	if len(id) != 64 {
		return result, errors.New("invalid container ID")
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return result, errors.New("invalid container ID")
		}
	}
	err := d.request(ctx, http.MethodGet, "/containers/"+id+"/stats?stream=false", &result)
	return result, err
}
func (d *DashboardClient) DashboardDiskUsage(ctx context.Context) (DashboardDisk, error) {
	var result DashboardDisk
	err := d.request(ctx, http.MethodGet, "/system/df", &result)
	return result, err
}
func (d *DashboardClient) PruneUnusedImages(ctx context.Context) (uint64, error) {
	var result struct{ SpaceReclaimed uint64 }
	// dangling=false is Engine's equivalent of docker image prune --all.
	// Docker retains every image referenced by a running OR stopped container.
	err := d.request(ctx, http.MethodPost, "/images/prune?filters="+url.QueryEscape(`{"dangling":["false"]}`), &result)
	return result.SpaceReclaimed, err
}
