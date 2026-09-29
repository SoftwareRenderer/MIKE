package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// The messages route answers a window, not the thread, and the window rides the X-Messages-* headers.

// seedWindow writes count rows oldest-first, one per second, and returns their ids in that order.
func seedWindow(t *testing.T, s *Server, projID, taskID string, count int) []string {
	t.Helper()
	base := time.Now().UTC().Add(-time.Duration(count) * time.Second)
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		m := seedMessage(t, s, projID, taskID, &types.TaskMessage{
			Role:      "user",
			Content:   fmt.Sprintf("row %02d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
		ids = append(ids, m.ID)
	}
	return ids
}

func readWindow(t *testing.T, s *Server, path string) ([]types.TaskMessage, http.Header) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var messages []types.TaskMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &messages); err != nil {
		t.Fatalf("decode window: %v", err)
	}
	return messages, rr.Header()
}

func TestMessagesRouteAnswersTheNewestWindow(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Windowed")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 7)
	// The task's own row (its prompt) is part of the thread.
	all, err := s.store.GetTaskMessages(taskID, "")
	if err != nil || len(all) < 8 {
		t.Fatalf("seed a thread: %d rows, %v", len(all), err)
	}

	messages, headers := readWindow(t, s, path+"?limit=3")
	if len(messages) != 3 {
		t.Fatalf("window holds %d rows, want 3: %v", len(messages), messages)
	}
	for i, m := range messages {
		if want := all[len(all)-3+i]; m.ID != want.ID {
			t.Fatalf("row %d of the window = %s, want %s", i, m.ID, want.ID)
		}
	}
	if headers.Get("X-Messages-More") != "true" {
		t.Errorf("X-Messages-More = %q, want true", headers.Get("X-Messages-More"))
	}
	if headers.Get("X-Messages-Before") == "" {
		t.Error("X-Messages-Before is missing while older rows exist")
	}

	// The default window is the store's own, and this thread is longer than it.
	def, headers := readWindow(t, s, path)
	if len(def) != store.MessageWindowDefault {
		t.Fatalf("default window holds %d rows, want %d", len(def), store.MessageWindowDefault)
	}
	if headers.Get("X-Messages-More") != "true" {
		t.Errorf("X-Messages-More = %q on a thread longer than the window, want true", headers.Get("X-Messages-More"))
	}
}

// A thread shorter than the window is answered whole, and says there is nothing older.
func TestMessagesRouteAnswersAShortThreadWhole(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Whole")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 2)
	all, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read the thread: %v", err)
	}

	whole, headers := readWindow(t, s, path)
	if len(whole) != len(all) {
		t.Fatalf("default window holds %d rows, want all %d", len(whole), len(all))
	}
	if headers.Get("X-Messages-More") != "false" {
		t.Errorf("X-Messages-More = %q on a thread that fits, want false", headers.Get("X-Messages-More"))
	}
	if headers.Get("X-Messages-Before") != "" {
		t.Errorf("X-Messages-Before = %q on a thread that fits, want absent", headers.Get("X-Messages-Before"))
	}
}

func TestMessagesRoutePagesByCursor(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Paged")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 10)
	all, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read the thread: %v", err)
	}

	seen := map[string]bool{}
	var firstPage []string
	// Pages arrive newest first and are chronological within themselves, so stacking them bottom-up
	// gives write order.
	var pages [][]string
	cursor := ""
	for page := 0; page < len(all)+1; page++ {
		query := path + "?limit=4"
		if cursor != "" {
			query += "&before=" + cursor
		}
		messages, headers := readWindow(t, s, query)
		var ids []string
		for _, m := range messages {
			if seen[m.ID] {
				t.Fatalf("row %s arrived on two pages", m.ID)
			}
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
		if len(ids) == 0 {
			t.Fatal("a page a caller was told exists answered with no rows")
		}
		pages = append(pages, ids)
		if page == 0 {
			firstPage = ids
		}
		if headers.Get("X-Messages-More") != "true" {
			break
		}
		cursor = headers.Get("X-Messages-Before")
	}
	var walked []string
	for i := len(pages) - 1; i >= 0; i-- {
		walked = append(walked, pages[i]...)
	}
	if len(walked) != len(all) {
		t.Fatalf("the pages cover %d of the thread's %d rows", len(walked), len(all))
	}
	for i, id := range walked {
		if all[i].ID != id {
			t.Fatalf("position %d of the stacked pages = %s, want the thread's %s", i, id, all[i].ID)
		}
	}
	if len(firstPage) == 0 || firstPage[len(firstPage)-1] != all[len(all)-1].ID {
		t.Fatalf("first page = %v, want it to end on %s", firstPage, all[len(all)-1].ID)
	}
}

func TestMessagesRouteRefusesABadWindowRequest(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Refused")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 2)

	for _, query := range []string{
		"limit=0", "limit=-1", "limit=" + fmt.Sprint(store.MessageWindowMax+1), "limit=lots",
		"before=not-a-cursor",
	} {
		rr := testResponseAuth(t, s, testRequest("GET", path+"?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %s", query, rr.Code, rr.Body.String())
		}
	}
}

// A window stays a window: the rows beyond it are not sent, and the rows it does send are whole answers.
func TestMessagesWindowHoldsOnlyItsWindow(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Bounded")
	seedWindow(t, s, projID, taskID, 12)

	messages, _ := readWindow(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/messages?limit=2")
	if len(messages) != 2 {
		t.Fatalf("window holds %d rows, want the 2 units asked for", len(messages))
	}
}

// The redaction pass runs over the window, and what it sends is still redacted.
func TestMessagesWindowStillRedacts(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Redacted")
	seedWindow(t, s, projID, taskID, 1)
	seedMessage(t, s, projID, taskID, &types.TaskMessage{
		Role:             "assistant",
		Content:          "writing it now",
		ReasoningContent: "a very long chain of thought that is worth suppressing " + string(make([]byte, 300)),
		CreatedAt:        time.Now().UTC(),
	})

	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages?limit=1"
	messages, _ := readWindow(t, s, path)
	newest := messages[len(messages)-1]
	if newest.Role != "assistant" {
		t.Fatalf("window ends on a %s row, want the newest assistant row", newest.Role)
	}
	if newest.ReasoningContent != "[HIDDEN]" || !newest.HasReasoning {
		t.Errorf("reasoning = %q (has_reasoning %v), want the suppressed form", newest.ReasoningContent, newest.HasReasoning)
	}

	full, _ := readWindow(t, s, path+"&full=1")
	if full[len(full)-1].ReasoningContent == "[HIDDEN]" || full[len(full)-1].ReasoningContent == "" {
		t.Errorf("full=1 lost the reasoning: %q", full[len(full)-1].ReasoningContent)
	}
}

// Opening the same task twice asks the same question, so the second answer is a 304 with no body. The
// handler owns the lifetime and CompressMiddleware owns the digest, so this route sets no ETag of its own.
func TestMessagesWindowIsStoredThenRevalidated(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Revalidated")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 4)

	get := func(url, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := testRequest("GET", url, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		return testResponseAuth(t, s, req)
	}

	first := get(path, "")
	if first.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, first.Code, first.Body.String())
	}
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the window carried no ETag, so a stored copy could not be revalidated")
	}

	reopen := get(path, etag)
	if reopen.Code != http.StatusNotModified {
		t.Fatalf("reopening an unchanged thread answered %d, want 304", reopen.Code)
	}
	if reopen.Body.Len() != 0 {
		t.Errorf("the 304 carried %d bytes of body", reopen.Body.Len())
	}
	for name, want := range map[string]string{"ETag": etag, "Cache-Control": "no-cache"} {
		if got := reopen.Header().Get(name); got != want {
			t.Errorf("the 304 repeated %s as %q, want %q", name, got, want)
		}
	}

	// A row that lands after the stored copy moves the digest, so the stored copy cannot be shown.
	seedMessage(t, s, projID, taskID, &types.TaskMessage{Role: "user", Content: "and now this"})
	moved := get(path, etag)
	if moved.Code == http.StatusNotModified {
		t.Fatal("a thread that gained a row still answered 304")
	}
	if moved.Header().Get("ETag") == etag {
		t.Error("the digest did not move when the thread did")
	}

	// Another window of the same thread is another answer, so it carries another digest.
	if other := get(path+"?limit=2", "").Header().Get("ETag"); other == etag {
		t.Error("?limit=2 shares the whole window's digest")
	}

	// A response holding provider keys is not worth storing anywhere.
	if got := get("/api/config", "").Header().Get("Cache-Control"); got != "" {
		t.Errorf("GET /api/config declares Cache-Control %q", got)
	}
}

// A floor bounds an older page, so the route refuses it without `before`.
func TestMessagesRouteRefusesAFloorAloneOrUnissued(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Floored")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	seedWindow(t, s, projID, taskID, 7)

	_, headers := readWindow(t, s, path)
	floor := url.QueryEscape(headers.Get("X-Messages-Before"))
	if floor == "" {
		t.Fatal("the newest window answered no cursor to walk from")
	}
	for _, query := range []string{
		"from=" + floor,
		"before=" + floor + "&from=not-a-cursor",
	} {
		rr := testResponseAuth(t, s, testRequest("GET", path+"?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %s", query, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "from") {
			t.Errorf("%s was refused without naming the parameter: %s", query, rr.Body.String())
		}
	}
}
