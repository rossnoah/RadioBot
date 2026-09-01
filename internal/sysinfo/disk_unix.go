//go:build linux || darwin

package sysinfo

import "syscall"

// diskUsage reports the size and free space of the filesystem holding path.
// Free space is what is available to an unprivileged user, not counting the
// reserved blocks only root can touch.
func diskUsage(path string) (total, free uint64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	// Bsize is int64 on Linux and int32 on Darwin; the conversion covers both.
	blockSize := uint64(stat.Bsize)
	return stat.Blocks * blockSize, stat.Bavail * blockSize, stat.Blocks > 0
}
