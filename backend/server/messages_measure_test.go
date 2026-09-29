package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// The size claim of the task-open window, measured: one synthetic 60-iteration turn is what a long agent
// session looks like in the table. `go test ./backend/server -run TaskOpenReads -v` re-measures it, and the
// assertion is the ratio rather than a byte count.

// iterationBytes is what one tool call's payload costs on the wire: 6 KiB of arguments and 6 KiB of output.
const iterationBytes = 6 << 10

// seedLongTurn writes one turn: the user row that opened it, then `iterations` assistant rows each
// carrying one tool call with its arguments and its result.
func seedLongTurn(t *testing.T, s *Server, projID, taskID string, iterations int) {
	t.Helper()
	seedLongTurns(t, s, projID, taskID, 1, iterations)
}

// seedLongTurns writes `turns` prompts, each opening `iterations` answers of its own.
func seedLongTurns(t *testing.T, s *Server, projID, taskID string, turns, iterations int) {
	t.Helper()
	body := strings.Repeat("func example() int { return 1 }\n", iterationBytes/29)
	base := time.Now().UTC()
	write := func(role, content, toolCalls, toolResult, assistantID string, at time.Time) {
		seedMessage(t, s, projID, taskID, &types.TaskMessage{
			Role: role, Content: content, ToolCalls: toolCalls, ToolResult: toolResult,
			AssistantMsgID: assistantID, CreatedAt: at,
		})
	}
	for turn := 0; turn < turns; turn++ {
		at := base.Add(time.Duration(turn*iterations+1) * time.Second)
		write("user", fmt.Sprintf("work through the tree %02d", turn), "", "", "", at)
		for i := 0; i < iterations; i++ {
			step := at.Add(time.Duration(i) * time.Second)
			assistant := fmt.Sprintf("assistant-%02d-%03d", turn, i)
			write("assistant", fmt.Sprintf("iteration %03d: reading, then editing", i), "", "", "", step)
			args, _ := json.Marshal(map[string]any{
				"tool_id": fmt.Sprintf("call-%02d-%03d", turn, i), "tool_name": "edit_file",
				"args": map[string]any{"path": fmt.Sprintf("file-%03d.go", i), "edits": body},
			})
			result, _ := json.Marshal(map[string]any{"output": body, "success": true, "duration": 12})
			write("tool_call", "", string(args), string(result), assistant, step.Add(time.Millisecond))
		}
	}
}

func readRaw(t *testing.T, s *Server, path string) (rows int, bytes int) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rr.Code, rr.Body.String())
	}
	var messages []types.TaskMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &messages); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return len(messages), rr.Body.Len()
}

func TestTaskOpenReadsAFractionOfItsThread(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Sixty iterations")
	const iterations = 60
	seedLongTurn(t, s, projID, taskID, iterations)
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	openRows, openBytes := readRaw(t, s, path)
	allRows, allBytes := readRaw(t, s, path+"?limit=25")
	whole, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read the thread: %v", err)
	}
	encoded, err := json.Marshal(whole)
	if err != nil {
		t.Fatalf("encode the thread: %v", err)
	}
	t.Logf("one open reads %d rows / %d KiB; the thread holds %d rows / %d KiB; the widest window the route answers is %d rows / %d KiB",
		openRows, openBytes>>10, len(whole), len(encoded)>>10, allRows, allBytes>>10)

	if openRows > 12 {
		t.Errorf("the default window read %d rows, want the newest 5 answers and their calls", openRows)
	}
	if openBytes*10 > len(encoded) {
		t.Errorf("one open read %d of the thread's %d bytes: a fifth of the conversation is not a window",
			openBytes, len(encoded))
	}
}

// jumpRead is one click's cost.
type jumpRead struct {
	reads int
	bytes int
	ids   []string
}

// jumpToPages pages older from the oldest row held until the named row arrives. A floor stops the walk at
// the row the client named rather than where a page happened to cut.
func jumpToPages(t *testing.T, s *Server, path, cursor, want, floor string) jumpRead {
	t.Helper()
	var got jumpRead
	before := cursor
	for page := 0; page < 200; page++ {
		query := "?before=" + url.QueryEscape(before)
		if floor != "" {
			query += "&from=" + url.QueryEscape(floor) + "&limit=" + strconv.Itoa(store.MessageWindowMax)
		}
		rr := testResponseAuth(t, s, testRequest("GET", path+query, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path+query, rr.Code, rr.Body.String())
		}
		var rows []types.TaskMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got.reads++
		got.bytes += rr.Body.Len()
		for _, m := range rows {
			got.ids = append(got.ids, m.ID)
		}
		for _, id := range got.ids {
			if id == want {
				return got
			}
		}
		before = rr.Header().Get("X-Messages-Before")
		if len(rows) == 0 || before == "" {
			return got
		}
	}
	t.Fatalf("the jump to %s never landed", want)
	return got
}

// The cost of one sticky-header jump, both shapes. `go test ./backend/server -run TestStickyJump -v`
// re-measures them.
func TestStickyJumpReadsOneFlooredWindow(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Long session")
	const turns, iterations = 8, 30
	seedLongTurns(t, s, projID, taskID, turns, iterations)
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	window, opened := readWindow(t, s, path)
	cursor := opened.Get("X-Messages-Before")
	if cursor == "" {
		t.Fatal("the window a task opens on answered no cursor to walk from")
	}
	prompts, _ := readTurns(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/turns")
	if len(prompts) < 2 {
		t.Fatalf("the fixture answered %d prompts", len(prompts))
	}
	named := prompts[len(prompts)-1]

	walked := jumpToPages(t, s, path, cursor, named.ID, "")
	floored := jumpToPages(t, s, path, cursor, named.ID, named.Cursor)
	t.Logf("one jump to the prompt above the open window: walked %d reads / %d rows / %d KiB; floored %d reads / %d rows / %d KiB",
		walked.reads, len(walked.ids), walked.bytes>>10, floored.reads, len(floored.ids), floored.bytes>>10)

	if !slices.Contains(floored.ids, named.ID) {
		t.Error("the floored read never answered the prompt the header named")
	}
	if floored.reads >= walked.reads {
		t.Errorf("the floor took %d reads, the walk %d: bounding the travel must not cost more", floored.reads, walked.reads)
	}
	if floored.bytes > walked.bytes {
		t.Errorf("the floored read carried %d bytes, the walk %d", floored.bytes, walked.bytes)
	}

	// The floored read answers exactly the rows between the two cursors: no gap, no overshoot.
	whole, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read the thread: %v", err)
	}
	between := rowsBetween(whole, named.ID, window[0].ID)
	if !slices.Equal(sortedCopy(between), sortedCopy(floored.ids)) {
		t.Errorf("the floored read answered %d rows, want the %d rows between the two cursors",
			len(floored.ids), len(between))
	}
	if len(walked.ids) <= len(between) {
		t.Errorf("the walk was expected to overshoot the floor, got %d rows for %d wanted", len(walked.ids), len(between))
	}
}

// rowsBetween answers the thread's rows from `from` inclusive up to `until` exclusive.
func rowsBetween(all []types.TaskMessage, from, until string) []string {
	var ids []string
	inside := false
	for _, m := range all {
		if m.ID == from {
			inside = true
		}
		if m.ID == until {
			return ids
		}
		if inside {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func sortedCopy(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
