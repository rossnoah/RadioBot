//go:build linux

package sysinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// residentMemory reads RSS from /proc/self/statm, whose second field is the
// resident set in pages.
func residentMemory() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

// hostUptime reads how long the machine has been up, whose first field in
// /proc/uptime is seconds since boot.
func hostUptime() time.Duration {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func loadAverage() ([3]float64, bool) {
	var load [3]float64

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return load, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return load, false
	}
	for i := 0; i < 3; i++ {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return load, false
		}
		load[i] = value
	}
	return load, true
}

// hostMemory reads MemTotal and MemAvailable from /proc/meminfo. MemAvailable
// is the useful figure — it accounts for reclaimable cache, which on a device
// that writes audio all day is most of what "used" memory looks like.
func hostMemory() (total, available uint64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}

	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch key {
		case "MemTotal":
			total = parseMemInfoKB(value)
		case "MemAvailable":
			available = parseMemInfoKB(value)
		}
	}
	return total, available, total > 0
}

// parseMemInfoKB reads a "   16384 kB" value into bytes.
func parseMemInfoKB(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}

// cpuTemperature reads the CPU thermal zone in millidegrees Celsius. A
// machine may expose several zones for different components, so prefer one
// that names the CPU and fall back to the first that reads.
func cpuTemperature() (float64, bool) {
	zones, err := filepath.Glob("/sys/class/thermal/thermal_zone*")
	if err != nil || len(zones) == 0 {
		return 0, false
	}

	var fallback float64
	var haveFallback bool

	for _, zone := range zones {
		raw, err := os.ReadFile(filepath.Join(zone, "temp"))
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			continue
		}
		celsius := milli / 1000

		kind, _ := os.ReadFile(filepath.Join(zone, "type"))
		name := strings.ToLower(strings.TrimSpace(string(kind)))
		// "cpu-thermal" on a Raspberry Pi, "x86_pkg_temp" elsewhere.
		if strings.Contains(name, "cpu") || strings.Contains(name, "pkg") {
			return celsius, true
		}
		if !haveFallback {
			fallback, haveFallback = celsius, true
		}
	}
	return fallback, haveFallback
}
