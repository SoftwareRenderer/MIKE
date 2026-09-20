package stream

// A frame is handed to a task's subscribers and added to that task's replay buffer as one step,
// and taking a task reads that buffer in the same step that makes the connection a subscriber.
// Splitting either step lets a subscribe land between the halves, and both outcomes are a frame
// that reaches nobody — a hole the client cannot see.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

func TestASubscribeDuringATurnNeitherLosesNorRepeatsTheFrame(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	// One task per round. The caps that exist to bound what a browser may claim would fail this
	// test for reasons unrelated to the interleaving, and tripping one looks just like a lost frame.
	asm.taskBufKeys = 100000
	asm.maxTaskSubs = 100000
	t.Cleanup(asm.Close)

	fs := dialStream(t, asm)
	client := clientAlone(t, asm)
	conn := connOf(t, asm)

	const rounds = 1200
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		// The first frame is sent while nobody holds the task, so only the replay can bring it;
		// the second races a subscribe for the same task.
		asm.SendTaskEvent("proj-race", taskID, tokenText(fmt.Sprintf("seeded-%d", i)))

		wg.Add(2)
		go func() {
			defer wg.Done()
			asm.SendTaskEvent("proj-race", taskID, tokenText(fmt.Sprintf("raced-%d", i)))
		}()
		go func() {
			defer wg.Done()
			asm.subscribeTask(conn, "proj-race", taskID)
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	counted := 0
	deadline := time.After(10 * time.Second)
	for counted < 2*rounds {
		select {
		case frame, ok := <-fs.frames:
			if !ok {
				t.Fatalf("the connection ended after %d of %d frames", counted, 2*rounds)
			}
			if frame["type"] != string(types.WSTypeTaskEvent) || frame["event"] != string(types.WSTypeToken) {
				continue
			}
			data, _ := frame["data"].(map[string]any)
			text, _ := data["text"].(string)
			seen[text]++
			counted++
		case <-deadline:
			t.Fatalf("%d of %d frames arrived within the window", counted, 2*rounds)
		}
	}

	// A frame the sender dropped is a different fault than a frame nobody was told about, and an
	// ended connection would otherwise be reported as the second.
	if missed := client.out.droppedFrames(); missed > 0 {
		t.Fatalf("the sender dropped %d frame(s): this test outran the queue, so it shows nothing about the interleaving", missed)
	}
	for i := 0; i < rounds; i++ {
		for _, text := range []string{fmt.Sprintf("seeded-%d", i), fmt.Sprintf("raced-%d", i)} {
			switch got := seen[text]; got {
			case 1:
			case 0:
				t.Fatalf("frame %q never reached the connection (%d of %d arrived)", text, counted, 2*rounds)
			default:
				t.Fatalf("frame %q reached the connection %d times, want exactly 1", text, got)
			}
		}
	}
}

// connOf returns the server's end of the one live connection, which is what the hub keys it by.
func connOf(t *testing.T, asm *AppStreamManager) *websocket.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		asm.mu.RLock()
		var found *websocket.Conn
		if len(asm.clients) == 1 {
			for conn := range asm.clients {
				found = conn
			}
		}
		asm.mu.RUnlock()
		if found != nil {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatal("no connection registered with the hub")
		}
		time.Sleep(time.Millisecond)
	}
}
