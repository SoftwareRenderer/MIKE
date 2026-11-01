//go:build unix

package fsutil

import (
	"path/filepath"
	"testing"
)

func TestDiskUsageForReportsTheFilesystemHoldingThePath(t *testing.T) {
	dir := t.TempDir()

	usage, err := DiskUsageFor(dir)
	if err != nil {
		t.Fatalf("DiskUsageFor(%q): %v", dir, err)
	}
	if usage.TotalBytes == 0 {
		t.Fatalf("total bytes is 0 for the filesystem holding %q", dir)
	}
	if usage.AvailBytes > usage.TotalBytes {
		t.Fatalf("available %d exceeds total %d", usage.AvailBytes, usage.TotalBytes)
	}
}

// A path that is not there answers an error, not an empty reading.
func TestDiskUsageForRefusesAPathThatDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")

	if usage, err := DiskUsageFor(missing); err == nil {
		t.Fatalf("DiskUsageFor(%q) answered %+v, want an error", missing, usage)
	}
}
