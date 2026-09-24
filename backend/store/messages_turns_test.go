package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The prompt read exists because a window counted in answers does not hold the prompt that opened the
// newest turn: it names that prompt anyway, and it costs the prompts it returns rather than the rows
// between them.

// seedTurns writes `turns` prompts, each followed by `answers` assistant rows carrying one tool call — the
// shape of a long agent turn, where every answer is its own unit of the window count.
func seedTurns(t *testing.T, s *Store, taskID, threadID string, turns, answers int) []string {
	t.Helper()
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask(taskID, "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	at := base
	var prompts []string
	for turn := 0; turn < turns; turn++ {
		at = at.Add(time.Second)
		prompt := newMessage(fmt.Sprintf("u-%02d", turn), taskID, "user", fmt.Sprintf("prompt %02d\nsecond line", turn))
		prompt.ThreadID, prompt.CreatedAt = threadID, at
		if err := s.CreateTaskMessage(prompt); err != nil {
			t.Fatalf("create prompt: %v", err)
		}
		prompts = append(prompts, prompt.ID)
		for i := 0; i < answers; i++ {
			answer := newMessage(fmt.Sprintf("a-%02d-%02d", turn, i), taskID, "assistant", "iteration")
			answer.ThreadID, answer.CreatedAt = threadID, at
			if err := s.CreateTaskMessage(answer); err != nil {
				t.Fatalf("create answer: %v", err)
			}
			call := newMessage(fmt.Sprintf("c-%02d-%02d", turn, i), taskID, "tool_call", "call")
			call.ThreadID, call.CreatedAt, call.AssistantMsgID = threadID, at, answer.ID
			if err := s.CreateTaskMessage(call); err != nil {
				t.Fatalf("create call: %v", err)
			}
		}
	}
	return prompts
}

// The case the header hits: the newest window of answers holds no prompt, and the turn's prompt is still
// named.
func TestGetUserTurnPageNamesThePromptTheWindowLeftOut(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 3, 40)

	window, err := s.GetTaskMessagePage("task-1", "thread-1", MessageWindowDefault, "")
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	for _, m := range window.Messages {
		if m.Role == "user" {
			t.Fatalf("the fixture stopped being the case this read is for: a %d-row window holds a prompt", len(window.Messages))
		}
	}

	page, err := s.GetUserTurnPage("task-1", "thread-1", TurnWindowDefault, "")
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if len(page.Turns) != 3 {
		t.Fatalf("page holds %d prompts, want the thread's 3: %+v", len(page.Turns), page.Turns)
	}
	for i, want := range []string{"u-00", "u-01", "u-02"} {
		if got := page.Turns[i]; got.ID != want {
			t.Errorf("turn %d = %s, want %s — a page is oldest first, like the rows it sits above", i, got.ID, want)
		}
	}
	if got := page.Turns[len(page.Turns)-1].Text; got != "prompt 02\nsecond line" {
		t.Errorf("quoted head = %q, want the prompt's text: the first line is what a client takes from it", got)
	}
	if page.HasMore || page.Before != "" {
		t.Errorf("a page that reached the first prompt still offers a step back: more=%v cursor=%q", page.HasMore, page.Before)
	}
}

// Prompts arrive a page at a time too, and the walk hands over each one exactly once.
func TestGetUserTurnPageWalksPromptsWithoutRepeatingOne(t *testing.T) {
	s := testStore(t)
	prompts := seedTurns(t, s, "task-1", "thread-1", 9, 2)

	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		if page > len(prompts) {
			t.Fatalf("pages did not terminate after %d reads", page)
		}
		got, err := s.GetUserTurnPage("task-1", "thread-1", 2, cursor)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got.Turns) == 0 && got.HasMore {
			t.Fatalf("page %d says older prompts exist but answers none", page)
		}
		for i := len(got.Turns) - 1; i >= 0; i-- {
			seen = append([]string{got.Turns[i].ID}, seen...)
		}
		if !got.HasMore {
			if got.Before != "" {
				t.Errorf("the last page names a cursor: %q", got.Before)
			}
			break
		}
		if got.Before == "" {
			t.Fatalf("page %d says older prompts exist but names no cursor", page)
		}
		cursor = got.Before
	}

	if strings.Join(seen, ",") != strings.Join(prompts, ",") {
		t.Errorf("the pages walked %v, want the prompts in the order they were written: %v", seen, prompts)
	}
}

// A prompt longer than the label bound is quoted up to it: the response is bounded before the bytes are
// read, and a pasted file cannot ride along just to name the turn it opened.
func TestGetUserTurnPageBoundsTheQuotedLine(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 1, 1)
	long := newMessage("u-long", "task-1", "user", strings.Repeat("x", TurnLabelChars*5))
	long.ThreadID, long.CreatedAt = "thread-1", time.Now().UTC()
	if err := s.CreateTaskMessage(long); err != nil {
		t.Fatalf("create the long prompt: %v", err)
	}

	page, err := s.GetUserTurnPage("task-1", "thread-1", TurnWindowDefault, "")
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if len(page.Turns) == 0 {
		t.Fatal("the newest prompt is missing from the page")
	}
	if got := len(page.Turns[len(page.Turns)-1].Text); got != TurnLabelChars {
		t.Errorf("quoted line is %d characters, want the bound of %d", got, TurnLabelChars)
	}
}

// A conversation whose rows are all answers has no prompt to name, and answers an empty list rather than
// no list: the route answers with this slice as the body a client iterates.
func TestGetUserTurnPageAnswersAnEmptyPageWhenNoPromptExists(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 0, 0)
	answer := newMessage("a-1", "task-1", "assistant", "an answer with no prompt above it")
	answer.ThreadID, answer.CreatedAt = "thread-1", time.Now().UTC()
	if err := s.CreateTaskMessage(answer); err != nil {
		t.Fatalf("create the answer: %v", err)
	}

	page, err := s.GetUserTurnPage("task-1", "thread-1", TurnWindowDefault, "")
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if page.Turns == nil {
		t.Error("the page has no list of prompts, want an empty one")
	}
	if len(page.Turns) != 0 || page.HasMore || page.Before != "" {
		t.Errorf("page = %+v, want no prompts and no step back", page)
	}
}

// A turn's prompts belong to its thread, and the task-scoped read sees the whole task.
func TestGetUserTurnPageReadsOneThreadOrTheWholeTask(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 2, 1)
	other := newMessage("u-other", "task-1", "user", "a prompt on another thread")
	other.ThreadID, other.CreatedAt = "thread-2", time.Now().UTC()
	if err := s.CreateTaskMessage(other); err != nil {
		t.Fatalf("create the other thread's prompt: %v", err)
	}

	thread, err := s.GetUserTurnPage("task-1", "thread-1", TurnWindowDefault, "")
	if err != nil {
		t.Fatalf("thread page: %v", err)
	}
	task, err := s.GetUserTurnPage("task-1", "", TurnWindowDefault, "")
	if err != nil {
		t.Fatalf("task page: %v", err)
	}
	if len(thread.Turns) != 2 {
		t.Errorf("the thread page holds %d prompts, want its own 2: %+v", len(thread.Turns), thread.Turns)
	}
	if len(task.Turns) != 3 {
		t.Errorf("the task page holds %d prompts, want all 3: %+v", len(task.Turns), task.Turns)
	}
}

// The bound of this read is the number of prompts asked for, so the plan must walk the partial user-row
// index — over the plain (scope, created_at) index the same read passes every row of the turn below it.
func TestGetUserTurnPagesReadThroughTheUserRowIndexes(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 4, 40)

	for _, read := range []struct {
		name  string
		scope string
		index string
	}{
		{"a thread", "thread-1", "idx_task_messages_user_thread"},
		{"a task", "", "idx_task_messages_user_task"},
	} {
		where, args := conversationWhere("task-1", read.scope, nil)
		query := userTurnCols + where + userTurnOnly + ` ` + newestFirstOrder + ` LIMIT ?`
		plan := explainPlan(t, s, query, append(append([]any{TurnLabelChars}, args...), TurnWindowMax)...)
		if !strings.Contains(plan, read.index) {
			t.Errorf("%s: the prompt read does not walk %s:\n%s", read.name, read.index, plan)
		}
		if strings.Contains(plan, "USE TEMP B-TREE") {
			t.Errorf("%s: the prompt read sorts in a temp B-tree:\n%s", read.name, plan)
		}
	}
}

func TestGetUserTurnPageRefusesACursorItDidNotIssue(t *testing.T) {
	s := testStore(t)
	seedTurns(t, s, "task-1", "thread-1", 2, 1)

	for _, bad := range []string{"not-a-cursor", "AAAA", "e30", "aaaaaaaaAAAAAAAAAAAAAAAAAAAAAAAAAAA-"} {
		if _, err := s.GetUserTurnPage("task-1", "thread-1", 2, bad); !errors.Is(err, ErrMessageCursor) {
			t.Errorf("cursor %q: got %v, want ErrMessageCursor", bad, err)
		}
	}
}
