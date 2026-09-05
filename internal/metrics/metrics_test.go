package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewUsesLinuxDefaults(t *testing.T) {
	collector := NewWithConfig(Config{})
	if collector.procRoot != defaultProcRoot {
		t.Fatalf("proc root = %q, want %q", collector.procRoot, defaultProcRoot)
	}
	if collector.filesystemRoot != defaultFilesystemRoot {
		t.Fatalf("filesystem root = %q, want %q", collector.filesystemRoot, defaultFilesystemRoot)
	}
	if collector.sampleInterval != defaultSampleInterval {
		t.Fatalf("sample interval = %s, want %s", collector.sampleInterval, defaultSampleInterval)
	}
}

func TestParseCPUStat(t *testing.T) {
	sample, err := parseCPUStat([]byte("cpu 100 20 30 40 10 5 2 1 0 0\ncpu0 50 10 15 20 5\ncpu1 50 10 15 20 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sample.total != 208 {
		t.Fatalf("total = %d, want 208", sample.total)
	}
	if sample.idle != 50 {
		t.Fatalf("idle = %d, want 50", sample.idle)
	}
	if sample.cpuCount != 2 {
		t.Fatalf("CPU count = %d, want 2", sample.cpuCount)
	}
}

func TestParseProcessStatHandlesParenthesesInName(t *testing.T) {
	process, err := parseProcessStat(processStat(42, "worker)pool", 12, 8, 9))
	if err != nil {
		t.Fatal(err)
	}
	if process.name != "worker)pool" {
		t.Fatalf("name = %q, want worker)pool", process.name)
	}
	if process.cpuTicks != 20 {
		t.Fatalf("CPU ticks = %d, want 20", process.cpuTicks)
	}
	if process.rssPages != 9 {
		t.Fatalf("RSS pages = %d, want 9", process.rssPages)
	}
}

func TestParseMemInfoUsesAvailableMemoryAndFreeMemory(t *testing.T) {
	memory, err := parseMemInfo([]byte("MemTotal:       1024 kB\nMemFree:         256 kB\nMemAvailable:    512 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{
		TotalBytes:     1024 * 1024,
		AvailableBytes: 512 * 1024,
		UsedBytes:      512 * 1024,
		FreeBytes:      256 * 1024,
	}
	if memory != want {
		t.Fatalf("memory = %#v, want %#v", memory, want)
	}
}

func TestCollectorSamplesAndRanksProcesses(t *testing.T) {
	procRoot := t.TempDir()
	writeProcSample(t, procRoot, "cpu 100 0 0 100 0\ncpu0 100 0 0 100 0\ncpu1 0 0 0 0 0\n", map[int]string{
		101: string(processStat(101, "cpu-worker", 10, 0, 10)),
		102: string(processStat(102, "memory-worker", 1, 0, 20)),
	})

	collector := NewWithConfig(Config{
		ProcRoot:       procRoot,
		FilesystemRoot: "/fixture",
		SampleInterval: 0,
	})
	collector.statfs = func(path string) (filesystemStats, error) {
		if path != "/fixture" {
			t.Fatalf("statfs path = %q, want /fixture", path)
		}
		return filesystemStats{blocks: 100, freeBlocks: 40, availableBlocks: 35, blockSize: 4096}, nil
	}
	collector.wait = func(context.Context, time.Duration) error {
		writeProcSample(t, procRoot, "cpu 100 0 10 140 0\ncpu0 100 0 5 140 0\ncpu1 0 0 5 0 0\n", map[int]string{
			101: string(processStat(101, "cpu-worker", 15, 0, 10)),
			102: string(processStat(102, "memory-worker", 2, 0, 100)),
		})
		return nil
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CPUUsagePercent != 20 {
		t.Fatalf("CPU usage = %v, want 20", snapshot.CPUUsagePercent)
	}
	if snapshot.Memory.UsedBytes != 512*1024 {
		t.Fatalf("used memory = %d, want %d", snapshot.Memory.UsedBytes, 512*1024)
	}
	if snapshot.Disk != (Disk{UsedBytes: 60 * 4096, AvailableBytes: 35 * 4096}) {
		t.Fatalf("disk = %#v, want used/free fixture values", snapshot.Disk)
	}
	if len(snapshot.TopCPU) != 2 || snapshot.TopCPU[0].PID != 101 {
		t.Fatalf("top CPU processes = %#v, want PID 101 first", snapshot.TopCPU)
	}
	if snapshot.TopCPU[0].CPUPercent != 20 {
		t.Fatalf("top CPU usage = %v, want 20", snapshot.TopCPU[0].CPUPercent)
	}
	if len(snapshot.TopMemory) != 2 || snapshot.TopMemory[0].PID != 102 {
		t.Fatalf("top memory processes = %#v, want PID 102 first", snapshot.TopMemory)
	}
	wantMemoryBytes := uint64(100 * os.Getpagesize())
	if snapshot.TopMemory[0].MemoryBytes != wantMemoryBytes {
		t.Fatalf("top memory bytes = %d, want %d", snapshot.TopMemory[0].MemoryBytes, wantMemoryBytes)
	}
	if snapshot.TopMemory[0].MemoryPercent <= snapshot.TopMemory[1].MemoryPercent {
		t.Fatalf("top memory percentages are not sorted: %#v", snapshot.TopMemory)
	}
}

func TestTopProcessesAreLimitedToTen(t *testing.T) {
	sample := hostSample{processes: make(map[int]processSample)}
	for pid := 1; pid <= 12; pid++ {
		sample.processes[pid] = processSample{name: fmt.Sprintf("process-%d", pid), rssPages: uint64(pid)}
	}
	processes := topMemoryProcesses(sample, uint64(100*os.Getpagesize()))
	if len(processes) != 10 {
		t.Fatalf("process count = %d, want 10", len(processes))
	}
	if processes[0].PID != 12 || processes[9].PID != 3 {
		t.Fatalf("top memory processes = %#v, want PIDs 12 through 3", processes)
	}
}

func processStat(pid int, name string, userTicks, systemTicks, rssPages int) []byte {
	fields := []string{
		"S", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0",
		fmt.Sprint(userTicks), fmt.Sprint(systemTicks), "0", "0", "20", "0", "1", "0", "100", "4096", fmt.Sprint(rssPages),
	}
	return []byte(fmt.Sprintf("%d (%s) %s\n", pid, name, strings.Join(fields, " ")))
}

func writeProcSample(t *testing.T, procRoot, cpu string, processes map[int]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(procRoot, "stat"), []byte(cpu), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "meminfo"), []byte("MemTotal:       1024 kB\nMemFree:         256 kB\nMemAvailable:    512 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for pid, contents := range processes {
		processRoot := filepath.Join(procRoot, fmt.Sprint(pid))
		if err := os.MkdirAll(processRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(processRoot, "stat"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
