package organizer

import "syscall"

// syscallEXDEV is the "cross-device link" error os.Rename returns when the
// source and destination are on different filesystems.
const syscallEXDEV = syscall.EXDEV
