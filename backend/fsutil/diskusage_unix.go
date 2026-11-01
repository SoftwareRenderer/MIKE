//go:build unix

package fsutil

import (
	"fmt"
	"syscall"
)

// DiskUsage is what one filesystem reports about itself.
type DiskUsage struct {
	TotalBytes uint64
	// AvailBytes excludes root-reserved blocks, so total minus avail counts them as used.
	AvailBytes uint64
}

// DiskUsageFor reads the filesystem holding path. Nothing under path is opened.
func DiskUsageFor(path string) (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskUsage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	// A non-positive block size would answer an empty filesystem.
	if st.Bsize <= 0 {
		return DiskUsage{}, fmt.Errorf("statfs %s: block size %d", path, st.Bsize)
	}
	block := uint64(st.Bsize)
	return DiskUsage{
		TotalBytes: st.Blocks * block,
		AvailBytes: st.Bavail * block,
	}, nil
}
