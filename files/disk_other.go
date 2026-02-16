//go:build !linux

package files

// diskUsage is a stub for non-Linux platforms.
func diskUsage(path string) (total, free uint64) {
	return 0, 0
}
