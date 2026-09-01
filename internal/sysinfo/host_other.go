//go:build !linux

package sysinfo

import "time"

// The host metrics come from procfs and sysfs. Off Linux they are simply
// unavailable, and the dashboard omits every field reported as absent — the
// deployment target is a Raspberry Pi, and everywhere else is a dev machine.

func residentMemory() uint64 { return 0 }

func hostUptime() time.Duration { return 0 }

func loadAverage() ([3]float64, bool) { return [3]float64{}, false }

func hostMemory() (total, available uint64, ok bool) { return 0, 0, false }

func cpuTemperature() (float64, bool) { return 0, false }
