//go:build windows

package channel

// setReuseAddr is a no-op on Windows.
// SO_REUSEADDR on Windows allows port stealing (different semantics than Unix),
// so we don't set it.
func setReuseAddr(fd uintptr) {
	// No-op on Windows.
}
