package stream

import (
	"testing"
	"time"

	"mike/backend/fsutil"
	"mike/backend/types"
)

// These drive the real socket, so the frame's bytes are the claim.

// fixedStorage answers one reading forever, so the test does not read this host's disk.
func fixedStorage(total, avail uint64) func() (fsutil.DiskUsage, bool) {
	return func() (fsutil.DiskUsage, bool) {
		return fsutil.DiskUsage{TotalBytes: total, AvailBytes: avail}, true
	}
}

func isStorageFrame(frame map[string]any) bool {
	return frame["type"] == string(types.WSTypeStorageUsage)
}

// A connection opened after the last task landed in Done has missed every push.
func TestANewConnectionIsGreetedWithTheFilesystemReading(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	fs := dialStream(t, asm)

	frame := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	if frame["total_bytes"] != float64(1000) || frame["avail_bytes"] != float64(150) {
		t.Fatalf("greeting reading = %v, want total 1000 and avail 150", frame)
	}
}

// A task moving anywhere but Done changes nothing on disk.
func TestTheFilesystemReadingFollowsATaskLandingInDone(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	fs := dialStream(t, asm)
	fs.await(t, 5*time.Second, isStorageFrame, 1, "the greeting reading")

	asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusReview})
	fs.quietWindow(t, 500*time.Millisecond, isStorageFrame, "a reading after an update to Review")

	asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusDone})
	fs.await(t, 5*time.Second, isStorageFrame, 1, "a reading after the task landed in Done")
}

// Clearing Done ends with the board refresh. Without it the count stays red over freed space.
func TestTheBoardRefreshATeardownEndsWithPushesAFilesystemReading(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 900))
	fs := dialStream(t, asm)
	fs.await(t, 5*time.Second, isStorageFrame, 1, "the greeting reading")

	asm.Broadcast("p1")
	frame := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading after a teardown settled")
	if frame["avail_bytes"] != float64(900) {
		t.Fatalf("reading after the board refresh = %v, avail 900", frame)
	}
}

// No probe, an unreadable root and a zero-size root all answer no frame.
func TestNothingToSaySendsNoFilesystemFrame(t *testing.T) {
	wirings := map[string]func(*AppStreamManager){
		"no probe is wired": func(*AppStreamManager) {},
		"the probe cannot read the root": func(asm *AppStreamManager) {
			asm.SetStorageProbe(func() (fsutil.DiskUsage, bool) { return fsutil.DiskUsage{}, false })
		},
		"the root reports no size": func(asm *AppStreamManager) {
			asm.SetStorageProbe(func() (fsutil.DiskUsage, bool) { return fsutil.DiskUsage{TotalBytes: 0, AvailBytes: 0}, true })
		},
	}
	for name, wire := range wirings {
		t.Run(name, func(t *testing.T) {
			asm := NewAppStreamManager(writeTestStore(t), nil)
			wire(asm)
			fs := dialStream(t, asm)
			// project_counts is broadcast at connect, so seeing it proves the socket carries frames.
			fs.await(t, 5*time.Second, func(frame map[string]any) bool {
				return frame["type"] == string(types.WSTypeProjectCounts)
			}, 1, "the connection's first frame")

			asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusDone})
			asm.Broadcast("p1")
			fs.quietWindow(t, 500*time.Millisecond, isStorageFrame, "a reading there was none to give")
		})
	}
}
