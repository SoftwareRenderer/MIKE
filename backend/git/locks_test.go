package git

// A lock that leaves its package can be stored and copied, and a caller that copies the owning struct
// swaps which mutexes future callers find - two callers then serialize on different locks while each
// thinks it holds the one protecting the repository. So the accessors return a release function, and
// these tests hold that line.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTryLockProjectWorktreeReleasesTheLateWinner: a waiter that times out must not stay on the
// lock, or every later CreateWorktree for that project times out behind it.
func TestTryLockProjectWorktreeReleasesTheLateWinner(t *testing.T) {
	wm := NewWorktreeManager(nil, nil)
	const projectID = "leaky-project"

	// Hold the lock the way a slow worktree creation would.
	holder, held := wm.tryLockProjectWorktree(projectID, time.Second)
	if !held {
		t.Fatal("could not take the lock to hold it")
	}

	release, locked := wm.tryLockProjectWorktree(projectID, 50*time.Millisecond)
	if locked {
		release()
		holder()
		t.Fatal("tryLockProjectWorktree acquired a lock that was held")
	}

	// The holder finishes after the waiter gave up. The late win must hand the lock straight back.
	holder()
	time.Sleep(200 * time.Millisecond)

	// An abandoned waiter would leave this second attempt blocking for its whole timeout.
	release2, locked2 := wm.tryLockProjectWorktree(projectID, 2*time.Second)
	if !locked2 {
		t.Fatal("the project stayed locked after a single timeout — an abandoned waiter is holding it")
	}
	release2()
}

// TestTryLockProjectWorktreeHappyPath pins the ordinary contract: acquired returns a release function
// that actually releases.
func TestTryLockProjectWorktreeHappyPath(t *testing.T) {
	wm := NewWorktreeManager(nil, nil)

	release, locked := wm.tryLockProjectWorktree("proj", 2*time.Second)
	if !locked {
		t.Fatal("tryLockProjectWorktree failed to acquire an uncontended lock")
	}
	if _, busy := wm.tryLockProjectWorktree("proj", 20*time.Millisecond); busy {
		release()
		t.Fatal("the lock was reported acquired but is not held")
	}
	release()
	again, ok := wm.tryLockProjectWorktree("proj", 2*time.Second)
	if !ok {
		t.Fatal("the release function did not release")
	}
	again()
}

// TestLockProjectStashAndMergeAreDistinctTables: the stash lock and the merge lock are separate
// domains with a documented acquisition order. If they ever shared a table, taking one would block
// the other and the ordering rule would silently become a self-deadlock.
func TestLockProjectStashAndMergeAreDistinctTables(t *testing.T) {
	wm := NewWorktreeManager(nil, nil)

	// The mechanism-level half: three domains, three tables. Keys are the same kind of string in all
	// three, so a shared table would make "stash" and "merge" block each other by accident and turn the
	// documented ordering rule into a self-deadlock.
	if &wm.stash.Table == &wm.merge.Table || &wm.stash.Table == &wm.worktree.Table || &wm.merge.Table == &wm.worktree.Table {
		t.Fatal("two git lock domains share one table")
	}
	// Types, not just addresses: two domains sharing a type would make `wm.stash = wm.merge` compile,
	// which is the fusion the keys cannot be trusted to reveal. Pointers are compared because
	// passing the value to reflect.TypeOf would itself copy the lock.
	if reflect.TypeOf(&wm.stash) == reflect.TypeOf(&wm.merge) {
		t.Fatal("stash and merge are the same type; assigning one to the other would compile")
	}

	releaseMerge := wm.LockProjectMerge("proj")
	defer releaseMerge()

	got := make(chan struct{})
	go func() {
		release := wm.LockProjectStash("proj")
		close(got)
		release()
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the stash lock waited on the merge lock — the two tables have been merged")
	}
}

// exportedMutexMethod matches an exported method that returns a raw mutex.
var exportedMutexMethod = regexp.MustCompile(`^func \([a-z_]+ \*?[A-Za-z_][A-Za-z0-9_]*\) [A-Z][A-Za-z0-9_]*\([^)]*\) \*sync\.Mutex`)

// TestNoExportedMutexAccessor keeps the encapsulation from rotting back: a comment saying "don't hand out
// locks" is worth nothing next to a test that fails when someone does.
//
// Unexported methods are fine - a mutex may be handed around inside the package that owns it - as long as
// it cannot cross a package boundary.
func TestNoExportedMutexAccessor(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string

	for _, sub := range []string{"backend", "pkg"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if relDir, _ := filepath.Rel(root, path); skipPath(relDir) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for lineNo := 0; sc.Scan(); lineNo++ {
				if exportedMutexMethod.MatchString(sc.Text()) {
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, lineNo+1, strings.TrimSpace(sc.Text())))
				}
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("%d exported method(s) hand a *sync.Mutex to another package; return a release func "+
			"instead (see WorktreeManager.LockProjectStash):\n\n%s\n",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
