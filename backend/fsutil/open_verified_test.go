package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestOpenVerifiedRefusesANameThatChangedUnderTheCheck: the check and the open are two syscalls, and
// whoever owns the directory can move other bytes under the name in between.
func TestOpenVerifiedRefusesANameThatChangedUnderTheCheck(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "f.txt")
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(name, []byte("checked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, name); err != nil {
		t.Fatal(err)
	}

	f, _, err := OpenVerified(name, before)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrSwapped) {
		t.Fatalf("OpenVerified on a swapped name = (%v, %v), want ErrSwapped", f, err)
	}
	// The refusal is answered from the error, so it must name no host path.
	if got := err.Error(); got != ErrSwapped.Error() {
		t.Errorf("refusal says %q, want the bare sentence", got)
	}

	// An unchanged name opens as itself.
	fresh, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	f, info, err := OpenVerified(name, fresh)
	if err != nil {
		t.Fatalf("OpenVerified on an unchanged name: %v", err)
	}
	defer f.Close()
	if info.Size() != fresh.Size() {
		t.Errorf("opened file reports %d bytes, the checked file %d", info.Size(), fresh.Size())
	}
}

// TestOpenVerifiedRefusesALinkPlantedInTheCheckIs the shape that matters for a worktree a task writes
// into: a regular file when jailed, a link to somewhere else by the open.
func TestOpenVerifiedRefusesALinkPlantedInTheCheck(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "f.txt")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(name, []byte("checked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("host secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, name); err != nil {
		t.Fatal(err)
	}

	f, _, err := OpenVerified(name, before)
	if f != nil {
		f.Close()
		t.Errorf("a link planted at the name was opened and handed back")
	}
	if !errors.Is(err, ErrSwapped) {
		t.Fatalf("OpenVerified through a planted link = %v, want ErrSwapped", err)
	}
}

// TestOpenVerifiedRefusesANameThatIsNotARegularFile: opening a FIFO for reading waits for a writer, so
// the refusal has to come before the open.
func TestOpenVerifiedRefusesANameThatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(name, 0o644); err != nil {
		t.Skipf("this filesystem cannot hold a FIFO, so an irregular name cannot be planted: %v", err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, _, err := OpenVerified(name, before)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("OpenVerified on a FIFO = %v, want ErrNotRegular", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OpenVerified never answered: it opened a FIFO instead of refusing it")
	}
}

// TestOpenCheckedCallsALinkAtTheNameASwapNotAnIrregularFile: Contained() refuses a name that is a link,
// so a link standing there by the open was planted after the check. Calling that "not a regular file"
// drops the swap from the log, the only place an attempt shows up.
func TestOpenCheckedCallsALinkAtTheNameASwapNotAnIrregularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("in project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "report.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	f, _, err := OpenChecked(link)
	if f != nil {
		f.Close()
		t.Error("a link standing at a jailed name was opened")
	}
	if !errors.Is(err, ErrSwapped) {
		t.Errorf("OpenChecked on a linked name = %v, want %v", err, ErrSwapped)
	}
}
