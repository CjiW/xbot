//go:build !windows

package channel

import "syscall"

// setReuseAddr enables SO_REUSEADDR on a socket file descriptor.
// Skipped on Windows where SO_REUSEADDR has different semantics.
func setReuseAddr(fd uintptr) {
	syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
