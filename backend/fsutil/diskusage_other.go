//go:build !unix

package fsutil

import "errors"

// DiskUsage is declared here too so the callers compile; see the unix file for its fields.
type DiskUsage struct {
	TotalBytes uint64
	AvailBytes uint64
}

var errNoDiskUsage = errors.New("fsutil: filesystem usage is not measured on this platform")

// DiskUsageFor always refuses: no portable reading on this platform.
func DiskUsageFor(path string) (DiskUsage, error) {
	return DiskUsage{}, errNoDiskUsage
}
