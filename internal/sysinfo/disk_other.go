//go:build !linux && !darwin

package sysinfo

// diskUsage needs statfs, which the remaining platforms do not share.
func diskUsage(string) (total, free uint64, ok bool) { return 0, 0, false }
