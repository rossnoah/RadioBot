// Package sysinfo collects the runtime and host metrics shown on the status
// page: how the process is behaving, and whether the machine underneath it is
// running out of something.
//
// Everything here degrades rather than fails. Host metrics come from Linux
// sysfs and procfs, so on a development Mac the corresponding fields are
// simply absent and the dashboard omits them.
package sysinfo

import (
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

// processStart is when this process began, for its own uptime.
var processStart = time.Now()

// Info is one snapshot of the process and its host.
type Info struct {
	Process Process
	Host    Host
	Build   Build
}

// Process describes this program's own resource use. It is worth watching on
// a device meant to run untouched for months: a slow leak shows up here as
// heap or goroutines that never come back down.
type Process struct {
	Uptime     time.Duration
	Goroutines int

	// RSS is resident memory as the OS sees it, which includes far more than
	// the Go heap. Zero when the platform cannot report it.
	RSS uint64

	HeapAlloc uint64 // live heap objects
	HeapSys   uint64 // heap memory obtained from the OS
	Sys       uint64 // total memory obtained from the OS

	NumGC        uint32
	LastGC       time.Time
	GCPauseTotal time.Duration
	GCCPUPercent float64
}

// Host describes the machine. Fields the platform cannot report are left
// zero, and Has* reports which of those are meaningful.
type Host struct {
	Hostname string
	OS       string
	Arch     string
	NumCPU   int

	Uptime time.Duration

	LoadAvg    [3]float64
	HasLoadAvg bool

	MemTotal     uint64
	MemAvailable uint64
	HasMemory    bool

	// Disk figures are for the filesystem holding the recordings, which is
	// the resource that actually runs out on these devices.
	DiskTotal uint64
	DiskFree  uint64
	HasDisk   bool

	// CPUTempC matters on a Raspberry Pi, where sustained load throttles the
	// clock long before anything crashes.
	CPUTempC float64
	HasTemp  bool
}

// Build identifies which binary is running, so a device can be told apart
// from the source tree it was built from.
type Build struct {
	GoVersion string
	Revision  string
	Time      string
	Modified  bool
}

// Collect takes a snapshot. diskPath selects the filesystem to report on;
// pass the recordings folder.
func Collect(diskPath string) Info {
	return Info{
		Process: collectProcess(),
		Host:    collectHost(diskPath),
		Build:   collectBuild(),
	}
}

func collectProcess() Process {
	// ReadMemStats briefly stops the world, which is fine at the rate the
	// status page is rendered (cached, and only on request).
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	p := Process{
		Uptime:       time.Since(processStart),
		Goroutines:   runtime.NumGoroutine(),
		RSS:          residentMemory(),
		HeapAlloc:    mem.HeapAlloc,
		HeapSys:      mem.HeapSys,
		Sys:          mem.Sys,
		NumGC:        mem.NumGC,
		GCPauseTotal: time.Duration(mem.PauseTotalNs),
		GCCPUPercent: mem.GCCPUFraction * 100,
	}
	if mem.LastGC > 0 {
		p.LastGC = time.Unix(0, int64(mem.LastGC))
	}
	return p
}

func collectHost(diskPath string) Host {
	h := Host{
		OS:     runtime.GOOS,
		Arch:   runtime.GOARCH,
		NumCPU: runtime.NumCPU(),
	}
	if name, err := os.Hostname(); err == nil {
		h.Hostname = name
	}

	h.Uptime = hostUptime()
	h.LoadAvg, h.HasLoadAvg = loadAverage()
	h.MemTotal, h.MemAvailable, h.HasMemory = hostMemory()
	h.CPUTempC, h.HasTemp = cpuTemperature()
	h.DiskTotal, h.DiskFree, h.HasDisk = diskUsage(diskPath)
	return h
}

func collectBuild() Build {
	b := Build{GoVersion: runtime.Version()}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			b.Revision = setting.Value
		case "vcs.time":
			b.Time = setting.Value
		case "vcs.modified":
			b.Modified = setting.Value == "true"
		}
	}
	return b
}

// ShortRevision is the abbreviated commit the binary was built from.
func (b Build) ShortRevision() string {
	if len(b.Revision) > 12 {
		return b.Revision[:12]
	}
	return b.Revision
}
