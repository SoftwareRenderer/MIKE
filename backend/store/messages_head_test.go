package store

import (
	"strconv"
	"testing"

	"mike/backend/types"
)

// The head read is the chat's "first message" control: one ascending read rather than one read per page.

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func unitsIn(msgs []types.TaskMessage) int {
	units := 0
	for _, m := range msgs {
		if startsUnit(m.Role) {
			units++
		}
	}
	return units
}

// seedLongAnswers writes turns the way a busy one looks: a prompt, an answer, a run of cards, and the
// closing answer — the run a page cut in the middle of would strand.
func seedLongAnswers(t *testing.T, s *Store, taskID, threadID string, turns, cards int) []string {
	t.Helper()
	if err := s.CreateProject(newProject("proj-long", "long", "/tmp/probe-long")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask(taskID, "proj-long")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	var ids []string
	i := 0
	write := func(role string) {
		m := newMessage("long-"+strconv.Itoa(i), taskID, role, role)
		m.ThreadID = threadID
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create %s row: %v", role, err)
		}
		ids = append(ids, m.ID)
		i++
	}
	for turn := 0; turn < turns; turn++ {
		write("user")
		write("assistant")
		for k := 0; k < cards; k++ {
			write("tool_call")
		}
		write("assistant")
	}
	return ids
}

func TestGetTaskMessageHeadPageAnswersTheOpeningPage(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 30)

	got, err := s.GetTaskMessageHeadPage("task-1", "thread-1", 2)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	// Two answers of a thread written user, assistant, tool_call: the prompt, the answer, and the card the
	// answer announced — the card belongs to the second answer, so it rides along with it.
	if want := ids[:3]; !sameIDs(idsOf(got.Messages), want) {
		t.Fatalf("head = %v, want %v", idsOf(got.Messages), want)
	}
}

func TestGetTaskMessageHeadPageEndsWhereAnAnswerEnds(t *testing.T) {
	s := testStore(t)
	seedLongAnswers(t, s, "task-long", "thread-long", 6, 5)
	all, err := s.GetTaskMessages("task-long", "thread-long")
	if err != nil {
		t.Fatalf("read thread: %v", err)
	}

	for limit := 1; limit <= 12; limit++ {
		head, err := s.GetTaskMessageHeadPage("task-long", "thread-long", limit)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(head.Messages) == 0 {
			t.Fatalf("limit %d answered no rows", limit)
		}
		if unitsIn(head.Messages) > limit {
			t.Fatalf("limit %d answered %d answers", limit, unitsIn(head.Messages))
		}
		if len(head.Messages) == len(all) {
			continue // the page reached the newest row: there is nothing below it to strand
		}
		last := head.Messages[len(head.Messages)-1]
		at := -1
		for i, m := range all {
			if m.ID == last.ID {
				at = i
				break
			}
		}
		if at < 0 {
			t.Fatalf("limit %d answered a row the thread does not hold: %s", limit, last.ID)
		}
		if next := all[at+1]; !startsUnit(next.Role) {
			t.Fatalf("limit %d ended inside an answer: %s (%s) opens the rows left behind", limit, next.ID, next.Role)
		}
	}
}

// The fill is the ordinary descending walk and the client drops rows it already holds, so head plus walk
// hands over the thread exactly once.
func TestGetTaskMessageHeadPageAndTheWalkHandOverEveryRowOnce(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 20)

	head, err := s.GetTaskMessageHeadPage("task-1", "thread-1", 3)
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	var pages [][]string
	cursor := ""
	for {
		page, err := s.GetTaskMessagePage("task-1", "thread-1", 3, cursor, "")
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		pages = append(pages, idsOf(page.Messages))
		if !page.HasMore {
			break
		}
		cursor = page.Before
	}

	held := make([]string, 0, len(ids))
	for p := len(pages) - 1; p >= 0; p-- {
		held = append(held, pages[p]...)
	}
	seen := make(map[string]bool, len(ids))
	merged := make([]string, 0, len(ids))
	for _, id := range append(append([]string{}, idsOf(head.Messages)...), held...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id)
	}
	if !sameIDs(merged, ids) {
		t.Fatalf("head + walk = %v, want the thread %v", merged, ids)
	}
}

func TestGetTaskMessageHeadPageOnAThreadShorterThanAPage(t *testing.T) {
	s := testStore(t)
	ids := seedThread(t, s, "task-1", "thread-1", 4)

	got, err := s.GetTaskMessageHeadPage("task-1", "thread-1", MessageWindowMax)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !sameIDs(idsOf(got.Messages), ids) {
		t.Fatalf("head = %v, want the whole thread %v", idsOf(got.Messages), ids)
	}
}

func TestGetTaskMessageHeadPageCountsAnswersAndBoundsTheLimit(t *testing.T) {
	s := testStore(t)
	seedThread(t, s, "task-1", "thread-1", 30)

	// A limit the route would refuse is the default, not an error: same rule as the window read.
	for _, limit := range []int{0, -1, MessageWindowMax + 1} {
		got, err := s.GetTaskMessageHeadPage("task-1", "thread-1", limit)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if units := unitsIn(got.Messages); units != MessageWindowDefault {
			t.Fatalf("limit %d answered %d answers, want the default %d", limit, units, MessageWindowDefault)
		}
	}

	// Answers, not rows: five cards under one answer are still one answer.
	got, err := s.GetTaskMessageHeadPage("task-1", "thread-1", 1)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if units := unitsIn(got.Messages); units != 1 {
		t.Fatalf("limit 1 answered %d answers", units)
	}
}

// A fork can leave a thread opening on a tool_call row. The page that starts at the thread keeps it.
func TestGetTaskMessageHeadPageKeepsWhatTheThreadOpensWith(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-open", "open", "/tmp/probe-open")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-open", "proj-open")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	var ids []string
	for i, role := range []string{"tool_call", "user", "assistant"} {
		m := newMessage("open-"+strconv.Itoa(i), "task-open", role, role)
		m.ThreadID = "thread-open"
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create %s row: %v", role, err)
		}
		ids = append(ids, m.ID)
	}

	got, err := s.GetTaskMessageHeadPage("task-open", "thread-open", 5)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !sameIDs(idsOf(got.Messages), ids) {
		t.Fatalf("head = %v, want %v", idsOf(got.Messages), ids)
	}
}
