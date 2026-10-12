package compose

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type dashboardTransport func(*http.Request) (*http.Response, error)

func (f dashboardTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDashboardEngineRequests(t *testing.T) {
	requests := 0
	client := &DashboardClient{client: &http.Client{Transport: dashboardTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "docker" {
			t.Fatal("unexpected Docker endpoint")
		}
		body := ""
		switch r.URL.Path {
		case "/containers/json":
			if r.Method != "GET" || r.URL.Query().Get("all") != "1" || r.URL.Query().Get("filters") != `{"label":["redlaunch.managed=true"]}` {
				t.Fatal("unsafe container query")
			}
			body = `[{"Id":"abc","Names":["/core"],"Labels":{"redlaunch.managed":"true"}}]`
		case "/system/df":
			body = `{"LayersSize":4096,"Images":[{"Id":"image"}],"Volumes":[{"Name":"data","UsageData":{"Size":1024,"RefCount":1}}]}`
		case "/images/prune":
			if r.Method != "POST" || r.URL.Query().Get("filters") != `{"dangling":["false"]}` {
				t.Fatal("must prune only unused images")
			}
			body = `{"SpaceReclaimed":2048}`
		default:
			body = `{"cpu_stats":{"cpu_usage":{"total_usage":20},"system_cpu_usage":100,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":10},"system_cpu_usage":80},"memory_stats":{"usage":1024,"stats":{"inactive_file":100}},"networks":{"eth0":{"rx_bytes":50,"tx_bytes":60}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	ctx := context.Background()
	containers, err := client.DashboardContainers(ctx)
	if err != nil || len(containers) != 1 {
		t.Fatal(containers, err)
	}
	disk, err := client.DashboardDiskUsage(ctx)
	if err != nil || disk.LayersSize != 4096 || disk.Volumes[0].UsageData.Size != 1024 {
		t.Fatal(disk, err)
	}
	stats, err := client.DashboardContainerStats(ctx, strings.Repeat("a", 64))
	if err != nil || stats.CPUStats.CPUUsage.TotalUsage != 20 || stats.Networks["eth0"].RxBytes != 50 {
		t.Fatal(stats, err)
	}
	reclaimed, err := client.PruneUnusedImages(ctx)
	if err != nil || reclaimed != 2048 {
		t.Fatal(reclaimed, err)
	}
	if _, err := client.DashboardContainerStats(ctx, "../../images/prune"); err == nil || requests != 4 {
		t.Fatal("invalid ID made request")
	}
}
func TestDashboardEngineErrorsAreSanitized(t *testing.T) {
	client := &DashboardClient{client: &http.Client{Transport: dashboardTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("secret diagnostic"))}, nil
	})}}
	_, err := client.PruneUnusedImages(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
