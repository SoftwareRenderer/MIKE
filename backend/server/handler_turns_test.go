package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"mike/backend/store"
	"mike/backend/types"
)

// The turns route names the prompts of a conversation. It exists because the window read counts answers,
// so the prompt that opened a long turn is not among the rows a task opens on.

type turnRow struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func readTurns(t *testing.T, s *Server, path string) ([]turnRow, http.Header) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var turns []turnRow
	if err := json.Unmarshal(rr.Body.Bytes(), &turns); err != nil {
		t.Fatalf("decode turns: %v", err)
	}
	return turns, rr.Header()
}

// The case the chat header hits: a task whose newest turn is long answers its window with answers only,
// and still names the prompt that opened the turn.
func TestTurnsRouteNamesThePromptTheWindowLeftOut(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Long turn")
	seedLongTurn(t, s, projID, taskID, 30)

	messages, _ := readWindow(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/messages")
	for _, m := range messages {
		if m.Role == "user" {
			t.Fatalf("the fixture stopped being the case this route is for: the window holds a %s row", m.Role)
		}
	}

	turns, headers := readTurns(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/turns")
	named := false
	for _, turn := range turns {
		if strings.HasPrefix(turn.Text, "work through the tree") {
			named = true
		}
	}
	if !named {
		t.Errorf("the prompt the turn opened with is not among the %d turns the route answered: %+v", len(turns), turns)
	}
	if headers.Get("X-Turns-More") != "false" {
		t.Errorf("X-Turns-More = %q, want false — the thread's two prompts both fit in one page", headers.Get("X-Turns-More"))
	}
}

// Prompts a page at a time, each one handed over once.
func TestTurnsRoutePagesOlderPrompts(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Paged")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/turns"
	const seeded = 7
	ids := seedWindow(t, s, projID, taskID, seeded)

	var walked []string
	cursor := ""
	for page := 0; ; page++ {
		if page > seeded+1 {
			t.Fatalf("pages did not terminate after %d reads", page)
		}
		query := path + "?limit=2"
		if cursor != "" {
			query += "&before=" + cursor
		}
		turns, headers := readTurns(t, s, query)
		for i := len(turns) - 1; i >= 0; i-- {
			walked = append([]string{turns[i].ID}, walked...)
		}
		if headers.Get("X-Turns-More") != "true" {
			if headers.Get("X-Turns-Before") != "" {
				t.Errorf("the last page names a cursor: %q", headers.Get("X-Turns-Before"))
			}
			break
		}
		cursor = headers.Get("X-Turns-Before")
		if cursor == "" {
			t.Fatal("older prompts are promised with no cursor to ask for them")
		}
	}

	// The task's own prompt is a row too, so the walk covers it beside the seeded ones.
	if len(walked) != seeded+1 {
		t.Fatalf("the pages walked %d prompts, want the task's prompt and the %d seeded ones", len(walked), seeded)
	}
	seen := map[string]bool{}
	for _, id := range walked {
		if seen[id] {
			t.Fatalf("prompt %s arrived on two pages", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("prompt %s never arrived", id)
		}
	}
}

// The bound is what keeps the response bounded: a prompt that is a pasted file is quoted up to the label
// bound, not shipped.
func TestTurnsRouteBoundsTheQuotedLine(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Bounded")
	seedMessage(t, s, projID, taskID, &types.TaskMessage{
		Role: "user", Content: strings.Repeat("x", store.TurnLabelChars*5),
	})

	turns, _ := readTurns(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/turns")
	if len(turns) == 0 {
		t.Fatal("the conversation answered with no prompts")
	}
	if got := len(turns[len(turns)-1].Text); got != store.TurnLabelChars {
		t.Errorf("quoted head is %d characters, want the bound of %d", got, store.TurnLabelChars)
	}
}

func TestTurnsRouteRefusesLimitsItDoesNotAnswer(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Refused")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/turns"

	for _, query := range []string{"limit=0", "limit=-1", "limit=" + fmt.Sprint(store.TurnWindowMax+1), "limit=lots"} {
		rr := testResponseAuth(t, s, testRequest("GET", path+"?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 — %s", query, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "limit") {
			t.Errorf("%s: the fault does not name the parameter: %s", query, rr.Body.String())
		}
	}
}

func TestTurnsRouteRefusesACursorItDidNotIssue(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Cursor")

	rr := testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/turns?before=not-a-cursor", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a cursor this route never issued answered %d, want 400 — %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "before") {
		t.Errorf("the fault does not name the parameter: %s", rr.Body.String())
	}
}

func TestTurnsRouteAnswersForATaskThatIsGone(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	rr := testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/no-such-task/turns", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("an unknown task answered %d, want 404 — %s", rr.Code, rr.Body.String())
	}
}
