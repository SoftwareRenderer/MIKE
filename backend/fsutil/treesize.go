package fsutil

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// WalkBudget bounds one tree walk. A walk that must finish needs a ceiling, and a caller that cannot
// show part of a figure needs to be told the walk stopped.
type WalkBudget struct {
	// MaxEntries caps how many entries the walk looks at, directories included. Zero is no cap.
	MaxEntries int
	// Timeout bounds the walk from the call; zero is no deadline. It is checked between entries, so a
	// wedged stat is not interrupted.
	Timeout time.Duration
}

// TreeUsage is what one directory tree holds.
type TreeUsage struct {
	// Bytes is the sum of every regular file's apparent size, so a sparse file counts for less than
	// the blocks it holds.
	Bytes int64
	// Complete says every entry was looked at; when it is false, Bytes is part of the tree.
	Complete bool
}

// TreeUsageFor sums the regular files under path, following no symlink and counting no irregular file.
// A budget that runs out answers Complete=false; a tree that cannot be read — a checkout torn down
// mid-walk — answers an error, so part of a figure is never reported as one.
func TreeUsageFor(path string, budget WalkBudget) (TreeUsage, error) {
	var (
		usage    TreeUsage
		visited  int
		stopped  bool
		deadline time.Time
	)
	if budget.Timeout > 0 {
		deadline = time.Now().Add(budget.Timeout)
	}
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("tree %s: %w", path, err)
		}
		if budget.MaxEntries > 0 {
			visited++
			if visited > budget.MaxEntries {
				stopped = true
				return filepath.SkipAll
			}
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			stopped = true
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("tree %s: %w", path, err)
		}
		usage.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return TreeUsage{}, err
	}
	usage.Complete = !stopped
	return usage, nil
}
