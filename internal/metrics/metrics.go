// Package metrics collects the host metrics displayed on the Dashboard page.
package metrics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProcRoot       = "/proc"
	defaultFilesystemRoot = "/"
	defaultSampleInterval = 100 * time.Millisecond
	maxProcesses          = 10
)

// Snapshot contains one point-in-time view of the host resources.
type Snapshot struct {
	CPUUsagePercent float64
	Memory          Memory
	Disk            Disk
	TopCPU          []Process
	TopMemory       []Process
	CollectedAt     time.Time
}

// Memory contains host memory values in bytes.
type Memory struct {
	TotalBytes     uint64
	AvailableBytes uint64
	UsedBytes      uint64
	FreeBytes      uint64
}

// Disk contains root filesystem values in bytes.
type Disk struct {
	UsedBytes      uint64
	AvailableBytes uint64
}

// Process contains the resource values for one process.
type Process struct {
	PID           int
	Name          string
	CPUPercent    float64
	MemoryBytes   uint64
	MemoryPercent float64
}

// Config controls the host paths and sampling interval used by a Collector.
// The default paths target the Linux host on which Redlaunch is running.
type Config struct {
	ProcRoot       string
	FilesystemRoot string
	SampleInterval time.Duration
}

// Collector reads Linux procfs and filesystem statistics for the Dashboard.
type Collector struct {
	procRoot       string
	filesystemRoot string
	sampleInterval time.Duration
	wait           func(context.Context, time.Duration) error
	statfs         func(string) (filesystemStats, error)
}

type cpuSample struct {
	total    uint64
	idle     uint64
	cpuCount int
}

type processSample struct {
	name     string
	cpuTicks uint64
	rssPages uint64
}

type filesystemStats struct {
	blocks          uint64
	freeBlocks      uint64
	availableBlocks uint64
	blockSize       uint64
}

// New returns a collector for the current Linux host.
func New() *Collector {
	return NewWithConfig(Config{})
}

// NewWithConfig returns a collector with explicit host paths and sampling
// settings. Empty values use the normal Linux defaults.
func NewWithConfig(config Config) *Collector {
	procRoot := config.ProcRoot
	if procRoot == "" {
		procRoot = defaultProcRoot
	}
	filesystemRoot := config.FilesystemRoot
	if filesystemRoot == "" {
		filesystemRoot = defaultFilesystemRoot
	}
	sampleInterval := config.SampleInterval
	if sampleInterval <= 0 {
		sampleInterval = defaultSampleInterval
	}
	return &Collector{
		procRoot:       procRoot,
		filesystemRoot: filesystemRoot,
		sampleInterval: sampleInterval,
		wait:           waitForContext,
		statfs:         statFilesystem,
	}
}

// Collect samples the host and returns the current resource usage. CPU and
// process usage are calculated from two short procfs samples so they reflect
// activity during the request rather than lifetime counters.
func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	if c == nil {
		return Snapshot{}, errors.New("metrics collector is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	before, err := c.readSample()
	if err != nil {
		return Snapshot{}, err
	}
	if err := c.wait(ctx, c.sampleInterval); err != nil {
		return Snapshot{}, err
	}
	after, err := c.readSample()
	if err != nil {
		return Snapshot{}, err
	}

	memory, err := c.readMemory()
	if err != nil {
		return Snapshot{}, err
	}
	disk, err := c.readDisk()
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		CPUUsagePercent: cpuUsagePercent(before.cpu, after.cpu),
		Memory:          memory,
		Disk:            disk,
		TopCPU:          topCPUProcesses(before, after, memory.TotalBytes),
		TopMemory:       topMemoryProcesses(after, memory.TotalBytes),
		CollectedAt:     time.Now(),
	}, nil
}

func (c *Collector) readSample() (hostSample, error) {
	contents, err := os.ReadFile(filepath.Join(c.procRoot, "stat"))
	if err != nil {
		return hostSample{}, fmt.Errorf("read CPU statistics: %w", err)
	}
	cpu, err := parseCPUStat(contents)
	if err != nil {
		return hostSample{}, err
	}
	return hostSample{
		cpu:       cpu,
		processes: c.readProcesses(),
	}, nil
}

type hostSample struct {
	cpu       cpuSample
	processes map[int]processSample
}

func (c *Collector) readProcesses() map[int]processSample {
	entries, err := os.ReadDir(c.procRoot)
	if err != nil {
		return map[int]processSample{}
	}
	processes := make(map[int]processSample)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(c.procRoot, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		process, err := parseProcessStat(contents)
		if err != nil {
			continue
		}
		processes[pid] = process
	}
	return processes
}

func (c *Collector) readMemory() (Memory, error) {
	contents, err := os.ReadFile(filepath.Join(c.procRoot, "meminfo"))
	if err != nil {
		return Memory{}, fmt.Errorf("read memory statistics: %w", err)
	}
	return parseMemInfo(contents)
}

func (c *Collector) readDisk() (Disk, error) {
	stats, err := c.statfs(c.filesystemRoot)
	if err != nil {
		return Disk{}, fmt.Errorf("read disk statistics: %w", err)
	}
	return diskFromFilesystemStats(stats)
}

func parseCPUStat(contents []byte) (cpuSample, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	var sample cpuSample
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" {
			if len(fields) < 5 {
				return cpuSample{}, errors.New("CPU statistics are incomplete")
			}
			for _, field := range fields[1:] {
				value, err := strconv.ParseUint(field, 10, 64)
				if err != nil {
					return cpuSample{}, fmt.Errorf("parse CPU statistics: %w", err)
				}
				if ^uint64(0)-sample.total < value {
					return cpuSample{}, errors.New("CPU statistics overflow")
				}
				sample.total += value
			}
			idle, err := strconv.ParseUint(fields[4], 10, 64)
			if err != nil {
				return cpuSample{}, fmt.Errorf("parse idle CPU statistics: %w", err)
			}
			sample.idle = idle
			if len(fields) > 5 {
				iowait, err := strconv.ParseUint(fields[5], 10, 64)
				if err != nil {
					return cpuSample{}, fmt.Errorf("parse CPU wait statistics: %w", err)
				}
				if ^uint64(0)-sample.idle < iowait {
					return cpuSample{}, errors.New("CPU idle statistics overflow")
				}
				sample.idle += iowait
			}
			continue
		}
		if strings.HasPrefix(fields[0], "cpu") && isDecimal(fields[0][3:]) {
			sample.cpuCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return cpuSample{}, fmt.Errorf("scan CPU statistics: %w", err)
	}
	if sample.total == 0 {
		return cpuSample{}, errors.New("CPU statistics are unavailable")
	}
	if sample.cpuCount == 0 {
		sample.cpuCount = 1
	}
	return sample, nil
}

func parseProcessStat(contents []byte) (processSample, error) {
	text := strings.TrimSpace(string(contents))
	firstSpace := strings.IndexByte(text, ' ')
	lastClose := strings.LastIndexByte(text, ')')
	if firstSpace < 1 || lastClose < firstSpace {
		return processSample{}, errors.New("process statistics are malformed")
	}
	pid, err := strconv.Atoi(text[:firstSpace])
	if err != nil || pid < 1 {
		return processSample{}, errors.New("process ID is malformed")
	}
	name := strings.TrimPrefix(text[firstSpace+1:lastClose], "(")
	if name == "" {
		name = "unknown"
	}
	fields := strings.Fields(text[lastClose+1:])
	if len(fields) < 22 {
		return processSample{}, errors.New("process statistics are incomplete")
	}
	userTicks, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return processSample{}, fmt.Errorf("parse process user time: %w", err)
	}
	systemTicks, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return processSample{}, fmt.Errorf("parse process system time: %w", err)
	}
	rssPages, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return processSample{}, fmt.Errorf("parse process memory: %w", err)
	}
	if rssPages < 0 {
		rssPages = 0
	}
	if ^uint64(0)-userTicks < systemTicks {
		return processSample{}, errors.New("process CPU statistics overflow")
	}
	return processSample{
		name:     name,
		cpuTicks: userTicks + systemTicks,
		rssPages: uint64(rssPages),
	}, nil
}

func parseMemInfo(contents []byte) (Memory, error) {
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.IndexByte(line, ':')
		if separator < 1 {
			continue
		}
		key := strings.TrimSpace(line[:separator])
		fields := strings.Fields(line[separator+1:])
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return Memory{}, fmt.Errorf("parse %s: %w", key, err)
		}
		multiplier := uint64(1)
		if len(fields) > 1 {
			switch fields[1] {
			case "kB":
				multiplier = 1024
			case "MB":
				multiplier = 1024 * 1024
			case "GB":
				multiplier = 1024 * 1024 * 1024
			}
		}
		if value != 0 && ^uint64(0)/value < multiplier {
			return Memory{}, fmt.Errorf("%s is too large", key)
		}
		values[key] = value * multiplier
	}
	if err := scanner.Err(); err != nil {
		return Memory{}, fmt.Errorf("scan memory statistics: %w", err)
	}
	total, ok := values["MemTotal"]
	if !ok || total == 0 {
		return Memory{}, errors.New("total memory is unavailable")
	}
	free := values["MemFree"]
	if free > total {
		free = total
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = free
	}
	if available > total {
		available = total
	}
	return Memory{
		TotalBytes:     total,
		AvailableBytes: available,
		UsedBytes:      total - available,
		FreeBytes:      free,
	}, nil
}

func diskFromFilesystemStats(stats filesystemStats) (Disk, error) {
	if stats.blockSize == 0 {
		return Disk{}, errors.New("filesystem block size is unavailable")
	}
	total, ok := multiplyUint64(stats.blocks, stats.blockSize)
	if !ok {
		return Disk{}, errors.New("filesystem size overflow")
	}
	free, ok := multiplyUint64(stats.freeBlocks, stats.blockSize)
	if !ok {
		return Disk{}, errors.New("filesystem free space overflow")
	}
	available, ok := multiplyUint64(stats.availableBlocks, stats.blockSize)
	if !ok {
		return Disk{}, errors.New("filesystem available space overflow")
	}
	if free > total {
		free = total
	}
	if available > total {
		available = total
	}
	return Disk{UsedBytes: total - free, AvailableBytes: available}, nil
}

func statFilesystem(path string) (filesystemStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemStats{}, err
	}
	return filesystemStats{
		blocks:          uint64(stat.Blocks),
		freeBlocks:      uint64(stat.Bfree),
		availableBlocks: uint64(stat.Bavail),
		blockSize:       uint64(stat.Bsize),
	}, nil
}

func cpuUsagePercent(before, after cpuSample) float64 {
	if after.total <= before.total {
		return 0
	}
	totalDelta := after.total - before.total
	idleDelta := uint64(0)
	if after.idle > before.idle {
		idleDelta = after.idle - before.idle
	}
	busyDelta := totalDelta
	if idleDelta < busyDelta {
		busyDelta -= idleDelta
	} else {
		busyDelta = 0
	}
	return clampPercent(float64(busyDelta) / float64(totalDelta) * 100)
}

func topCPUProcesses(before, after hostSample, totalMemory uint64) []Process {
	totalDelta := uint64(0)
	if after.cpu.total > before.cpu.total {
		totalDelta = after.cpu.total - before.cpu.total
	}
	cpuCount := after.cpu.cpuCount
	if cpuCount < 1 {
		cpuCount = 1
	}
	processes := make([]Process, 0, len(after.processes))
	for pid, current := range after.processes {
		previous, ok := before.processes[pid]
		var delta uint64
		if ok && current.cpuTicks > previous.cpuTicks {
			delta = current.cpuTicks - previous.cpuTicks
		}
		percent := float64(0)
		if totalDelta > 0 {
			percent = float64(delta) / float64(totalDelta) * float64(cpuCount) * 100
		}
		processes = append(processes, Process{
			PID:           pid,
			Name:          current.name,
			CPUPercent:    nonNegativePercent(percent),
			MemoryBytes:   processMemoryBytes(current.rssPages),
			MemoryPercent: memoryPercent(current.rssPages, totalMemory),
		})
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].CPUPercent == processes[j].CPUPercent {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].CPUPercent > processes[j].CPUPercent
	})
	return limitProcesses(processes)
}

func topMemoryProcesses(sample hostSample, totalMemory uint64) []Process {
	processes := make([]Process, 0, len(sample.processes))
	for pid, current := range sample.processes {
		processes = append(processes, Process{
			PID:           pid,
			Name:          current.name,
			MemoryBytes:   processMemoryBytes(current.rssPages),
			MemoryPercent: memoryPercent(current.rssPages, totalMemory),
		})
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].MemoryBytes == processes[j].MemoryBytes {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].MemoryBytes > processes[j].MemoryBytes
	})
	return limitProcesses(processes)
}

func processMemoryBytes(pages uint64) uint64 {
	pageSize := uint64(os.Getpagesize())
	if pageSize == 0 {
		return 0
	}
	bytes, ok := multiplyUint64(pages, pageSize)
	if !ok {
		return ^uint64(0)
	}
	return bytes
}

func memoryPercent(pages, totalMemory uint64) float64 {
	if totalMemory == 0 {
		return 0
	}
	return clampPercent(float64(processMemoryBytes(pages)) / float64(totalMemory) * 100)
}

func limitProcesses(processes []Process) []Process {
	if len(processes) > maxProcesses {
		return processes[:maxProcesses]
	}
	return processes
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func multiplyUint64(left, right uint64) (uint64, bool) {
	if left != 0 && ^uint64(0)/left < right {
		return 0, false
	}
	return left * right, true
}

func clampPercent(value float64) float64 {
	value = nonNegativePercent(value)
	if value > 100 {
		return 100
	}
	return value
}

func nonNegativePercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
