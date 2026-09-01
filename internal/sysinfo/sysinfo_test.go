package sysinfo

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCollectProcess(t *testing.T) {
	info := Collect(t.TempDir())
	p := info.Process

	if p.Uptime <= 0 {
		t.Errorf("uptime = %v, want positive", p.Uptime)
	}
	if p.Goroutines < 1 {
		t.Errorf("goroutines = %d, want at least 1", p.Goroutines)
	}
	if p.HeapAlloc == 0 {
		t.Error("heap allocation reported as zero")
	}
	if p.Sys < p.HeapSys {
		t.Errorf("total system memory (%d) is below heap system memory (%d)", p.Sys, p.HeapSys)
	}
	if p.GCCPUPercent < 0 || p.GCCPUPercent > 100 {
		t.Errorf("GC CPU = %v%%, want a percentage", p.GCCPUPercent)
	}
}

// TestGCFieldsAdvance checks the numbers actually track the collector rather
// than being read once and frozen.
func TestGCFieldsAdvance(t *testing.T) {
	before := Collect(t.TempDir()).Process

	// Make some garbage and collect it.
	for i := 0; i < 5; i++ {
		sink = make([]byte, 1<<20)
		runtime.GC()
	}

	after := Collect(t.TempDir()).Process
	if after.NumGC <= before.NumGC {
		t.Errorf("NumGC did not advance: %d then %d", before.NumGC, after.NumGC)
	}
	if after.LastGC.IsZero() {
		t.Error("LastGC is unset after a collection")
	}
	if after.GCPauseTotal < before.GCPauseTotal {
		t.Error("total GC pause went backwards")
	}
}

// sink keeps the test's allocations from being optimised away.
var sink []byte

func TestCollectHost(t *testing.T) {
	host := Collect(t.TempDir()).Host

	if host.OS != runtime.GOOS || host.Arch != runtime.GOARCH {
		t.Errorf("host = %s/%s, want %s/%s", host.OS, host.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if host.NumCPU < 1 {
		t.Errorf("NumCPU = %d", host.NumCPU)
	}
	if host.Hostname == "" {
		t.Error("hostname is empty")
	}

	// Optional fields must be self-consistent: a field flagged present has to
	// carry a plausible value.
	if host.HasMemory {
		if host.MemTotal == 0 {
			t.Error("memory reported present with zero total")
		}
		if host.MemAvailable > host.MemTotal {
			t.Errorf("available memory (%d) exceeds total (%d)", host.MemAvailable, host.MemTotal)
		}
	}
	if host.HasTemp && (host.CPUTempC <= -50 || host.CPUTempC > 150) {
		t.Errorf("CPU temperature = %v C, want something plausible", host.CPUTempC)
	}
	if host.HasLoadAvg {
		for i, value := range host.LoadAvg {
			if value < 0 {
				t.Errorf("load average [%d] = %v", i, value)
			}
		}
	}
}

// TestDiskUsage is the metric that matters most on these devices: the SD card
// filling with recordings is the usual way they die.
func TestDiskUsage(t *testing.T) {
	host := Collect(t.TempDir()).Host

	if !host.HasDisk {
		t.Skip("disk statistics are unavailable on this platform")
	}
	if host.DiskTotal == 0 {
		t.Error("disk reported present with zero capacity")
	}
	if host.DiskFree > host.DiskTotal {
		t.Errorf("free space (%d) exceeds capacity (%d)", host.DiskFree, host.DiskTotal)
	}
}

// TestDiskUsageMissingPath must not report a made-up filesystem.
func TestDiskUsageMissingPath(t *testing.T) {
	if _, _, ok := diskUsage("/definitely/not/a/real/path/anywhere"); ok {
		t.Error("reported disk statistics for a path that does not exist")
	}
}

func TestCollectBuild(t *testing.T) {
	build := Collect(t.TempDir()).Build

	if !strings.HasPrefix(build.GoVersion, "go") {
		t.Errorf("Go version = %q", build.GoVersion)
	}
	// Revision is only stamped for a binary built inside a repository, so its
	// absence under `go test` is expected; the shortening must still behave.
	if len(build.ShortRevision()) > 12 {
		t.Errorf("short revision = %q, want at most 12 characters", build.ShortRevision())
	}
}

func TestShortRevision(t *testing.T) {
	tests := []struct{ full, want string }{
		{"", ""},
		{"abc123", "abc123"},
		{"e573973a1b2c3d4e5f6071829304a5b6c7d8e9f0", "e573973a1b2c"},
	}
	for _, tt := range tests {
		if got := (Build{Revision: tt.full}).ShortRevision(); got != tt.want {
			t.Errorf("ShortRevision(%q) = %q, want %q", tt.full, got, tt.want)
		}
	}
}

// TestCollectIsCheap keeps the status page from becoming expensive to render.
func TestCollectIsCheap(t *testing.T) {
	start := time.Now()
	for i := 0; i < 20; i++ {
		Collect(t.TempDir())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("20 collections took %v, want well under 2s", elapsed)
	}
}
