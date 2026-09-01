package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/rossnoah/radiobot/internal/sysinfo"
)

// Thresholds at which a meter changes colour. Disk and memory are about
// running out; the temperature ones bracket where a Raspberry Pi starts
// throttling its clock.
const (
	meterWarnPercent     = 80
	meterCriticalPercent = 90

	tempWarnC     = 70.0
	tempCriticalC = 80.0
)

// meterView is a labelled usage bar.
type meterView struct {
	Label   string
	Detail  string
	Percent int
	Width   template.CSS
	Class   string
}

// systemView is the host card: the machine the receiver runs on.
type systemView struct {
	Hostname    string
	Platform    string
	Uptime      string
	LoadAvg     string
	Memory      *meterView
	Disk        *meterView
	Temperature string
	TempClass   string
}

// processView is the runtime card. On a device meant to run untouched for
// months, this is where a slow leak becomes visible.
type processView struct {
	Uptime       string
	Goroutines   int
	RSS          string
	HeapAlloc    string
	HeapSys      string
	Sys          string
	NumGC        uint32
	LastGC       string
	GCPauseTotal string
	GCCPU        string
	GoVersion    string
	Revision     string
	Dirty        bool
	BuildTime    string
}

// newSystemView renders the host metrics, omitting anything the platform
// could not report.
func newSystemView(host sysinfo.Host) systemView {
	view := systemView{
		Hostname: host.Hostname,
		Platform: fmt.Sprintf("%s/%s, %d CPU%s",
			host.OS, host.Arch, host.NumCPU, plural(host.NumCPU)),
	}

	if host.Uptime > 0 {
		view.Uptime = formatCompactDuration(host.Uptime)
	}
	if host.HasLoadAvg {
		view.LoadAvg = fmt.Sprintf("%.2f, %.2f, %.2f",
			host.LoadAvg[0], host.LoadAvg[1], host.LoadAvg[2])
	}
	if host.HasMemory {
		used := host.MemTotal - host.MemAvailable
		view.Memory = newMeter(used, host.MemTotal, "available")
		view.Memory.Detail = fmt.Sprintf("%s of %s used, %s available",
			formatBytes(used), formatBytes(host.MemTotal), formatBytes(host.MemAvailable))
	}
	if host.HasDisk {
		used := host.DiskTotal - host.DiskFree
		view.Disk = newMeter(used, host.DiskTotal, "free")
		view.Disk.Detail = fmt.Sprintf("%s of %s used, %s free",
			formatBytes(used), formatBytes(host.DiskTotal), formatBytes(host.DiskFree))
	}
	if host.HasTemp {
		view.Temperature = fmt.Sprintf("%.1f °C", host.CPUTempC)
		view.TempClass = temperatureClass(host.CPUTempC)
	}
	return view
}

func newProcessView(proc sysinfo.Process, build sysinfo.Build) processView {
	view := processView{
		Uptime:       formatCompactDuration(proc.Uptime),
		Goroutines:   proc.Goroutines,
		HeapAlloc:    formatBytes(proc.HeapAlloc),
		HeapSys:      formatBytes(proc.HeapSys),
		Sys:          formatBytes(proc.Sys),
		NumGC:        proc.NumGC,
		GCPauseTotal: formatPause(proc.GCPauseTotal),
		GCCPU:        fmt.Sprintf("%.3f%%", proc.GCCPUPercent),
		GoVersion:    strings.TrimPrefix(build.GoVersion, "go"),
		Revision:     build.ShortRevision(),
		Dirty:        build.Modified,
		BuildTime:    build.Time,
	}
	if proc.RSS > 0 {
		view.RSS = formatBytes(proc.RSS)
	}
	if proc.LastGC.IsZero() {
		view.LastGC = "never"
	} else {
		view.LastGC = formatAgo(int(time.Since(proc.LastGC).Seconds()))
	}
	return view
}

// newMeter builds a usage bar. It guards against a zero total so a filesystem
// that reports nothing cannot divide by zero.
func newMeter(used, total uint64, remainingWord string) *meterView {
	percent := 0
	if total > 0 {
		percent = int(used * 100 / total)
	}
	if percent > 100 {
		percent = 100
	}

	return &meterView{
		Label:   fmt.Sprintf("%d%%", percent),
		Percent: percent,
		Width:   template.CSS(fmt.Sprintf("width: %d%%", percent)),
		Class:   meterClass(percent),
	}
}

func meterClass(percent int) string {
	switch {
	case percent >= meterCriticalPercent:
		return "bg-red-500"
	case percent >= meterWarnPercent:
		return "bg-amber-500"
	default:
		return "bg-indigo-500"
	}
}

func temperatureClass(celsius float64) string {
	switch {
	case celsius >= tempCriticalC:
		return "text-red-600 dark:text-red-400"
	case celsius >= tempWarnC:
		return "text-amber-600 dark:text-amber-400"
	default:
		return "text-gray-800 dark:text-gray-200"
	}
}

// formatBytes renders a byte count in the largest unit that keeps it above 1.
func formatBytes(bytes uint64) string {
	value := float64(bytes)
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f PB", value)
}

// formatCompactDuration renders an uptime at two levels of precision, which
// is as much as anyone reads off a status page.
func formatCompactDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd %dh", days, hours)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// formatPause renders cumulative GC pause time, which is normally tiny.
func formatPause(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
