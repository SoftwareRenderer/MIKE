package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestMessagesRouteHeadAnswersTheOpeningPage(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Headed")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 9)
	all, err := s.store.GetTaskMessages(taskID, "")
	if err != nil || len(all) < 10 {
		t.Fatalf("seed a thread: %d rows, %v", len(all), err)
	}

	messages, headers := readWindow(t, s, path+"?head=1&limit=3")
	if len(messages) != 3 {
		t.Fatalf("the head holds %d rows, want 3: %v", len(messages), messages)
	}
	// The thread's own first rows, in the order they were written.
	for i, m := range messages {
		if want := all[i]; m.ID != want.ID {
			t.Fatalf("row %d of the head = %s, want the thread's first row %s", i, m.ID, want.ID)
		}
	}

	// Neither header a walk runs on rides a head answer, and the window read still carries both: without
	// the contrast, this assertion would pass on a route that simply never sets them.
	if got := headers.Get("X-Messages-More"); got != "" {
		t.Fatalf("a head answer promised older rows: %q", got)
	}
	if got := headers.Get("X-Messages-Before"); got != "" {
		t.Fatalf("a head answer handed out a cursor: %q", got)
	}
	_, windowHeaders := readWindow(t, s, path+"?limit=3")
	if windowHeaders.Get("X-Messages-More") == "" || windowHeaders.Get("X-Messages-Before") == "" {
		t.Fatalf("the window read itself stopped carrying its headers: %v", windowHeaders)
	}
}

func TestMessagesRouteHeadAnswersAShortThreadWhole(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Short head")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 2)
	all, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read thread: %v", err)
	}

	messages, _ := readWindow(t, s, path+"?head=1")
	if len(messages) != len(all) {
		t.Fatalf("the head holds %d rows, want the whole thread of %d", len(messages), len(all))
	}
}

func TestMessagesRouteHeadRefusesAWindowCut(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Contradicted")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 9)
	_, headers := readWindow(t, s, path+"?limit=1")
	cursor := headers.Get("X-Messages-Before")
	if cursor == "" {
		t.Fatalf("the window read handed out no cursor to contradict with")
	}

	for _, cut := range []string{"before", "from"} {
		rr := testResponseAuth(t, s, testRequest("GET", path+"?head=1&"+cut+"="+cursor, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s with head=1: %d %s, want 400", cut, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), cut) {
			t.Fatalf("%s with head=1 answered %q, which does not name the parameter", cut, rr.Body.String())
		}
	}
}

func TestMessagesRouteHeadHonoursTheWindowCeiling(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Over the ceiling")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	rr := testResponseAuth(t, s, testRequest("GET", path+"?head=1&limit=1000", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a limit over the ceiling: %d %s, want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "limit") {
		t.Fatalf("the refusal %q does not name limit", rr.Body.String())
	}
}
