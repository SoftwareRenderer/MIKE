package server

import (
	"encoding/json/v2"
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

// The window is the newest N units of the conversation, oldest first, and its two headers say whether older
// content exists and where to ask for it. A thread that fits the window is answered whole and promises
// nothing older.
func TestMessagesRouteAnswersTheNewestWindow(t *testing.T) {
	const wholeThread = -1

	cases := []struct {
		name       string
		seed       int
		query      string
		wantRows   int
		wantMore   string
		wantCursor bool
	}{
		{"a cut window", 7, "?limit=3", 3, "true", true},
		{"the window the route answers unasked", 7, "", store.MessageWindowDefault, "true", true},
		{"a thread that fits", 2, "", wholeThread, "false", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Windowed")
			path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
			seedWindow(t, s, projID, taskID, tc.seed)
			// The task's own row (its prompt) is part of the thread.
			all, err := s.store.GetTaskMessages(taskID, "")
			if err != nil || len(all) < tc.seed+1 {
				t.Fatalf("seed a thread: %d rows, %v", len(all), err)
			}

			messages, headers := readWindow(t, s, path+tc.query)
			want := tc.wantRows
			if want == wholeThread {
				want = len(all)
			}
			if len(messages) != want {
				t.Fatalf("the window holds %d rows, want %d: %v", len(messages), want, messages)
			}
			// Chronological, and the newest of the thread — a cut that fell at the wrong end reads as
			// a conversation that never happened.
			for i, m := range messages {
				if older := all[len(all)-len(messages)+i]; m.ID != older.ID {
					t.Fatalf("row %d of the window = %s, want the thread's %s", i, m.ID, older.ID)
				}
			}
			if got := headers.Get("X-Messages-More"); got != tc.wantMore {
				t.Errorf("X-Messages-More = %q, want %q", got, tc.wantMore)
			}
			if got := headers.Get("X-Messages-Before") != ""; got != tc.wantCursor {
				t.Errorf("X-Messages-Before present = %v, want %v", got, tc.wantCursor)
			}
		})
	}
}

func TestMessagesRoutePagesByCursor(t *testing.T) {
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

// The two reads a returning tab repeats are asked on every return, so each must be answerable with no body
// when it says nothing moved, and must not be kept. The handler owns the lifetime and CompressMiddleware
// owns the digest, so neither route sets an ETag of its own, and a durable write moves the digest the
// stored copy was revalidated against.
func TestARepeatingReadIsAnsweredFromARevalidatableCopy(t *testing.T) {
	cases := []struct {
		name  string
		path  func(projID, taskID string) string
		write func(t *testing.T, s *Server, projID, taskID string)
		other string
	}{
		{
			name: "the message window",
			path: func(p, taskID string) string { return "/api/projects/" + p + "/tasks/" + taskID + "/messages" },
			write: func(t *testing.T, s *Server, p, taskID string) {
				seedMessage(t, s, p, taskID, &types.TaskMessage{Role: "user", Content: "and now this"})
			},
			// Another window of the same thread is another answer, so it carries another digest.
			other: "?limit=2",
		},
		{
			name:  "the task sync digest",
			path:  func(p, taskID string) string { return "/api/projects/" + p + "/tasks/" + taskID + "/sync" },
			write: func(t *testing.T, s *Server, p, taskID string) { s.appStreamMgr.DurableThrough(p, taskID) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Revalidated")
			seedWindow(t, s, projID, taskID, 4)
			path := tc.path(projID, taskID)

			first := getRevalidating(t, s, path, "")
			if first.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %s", path, first.Code, first.Body.String())
			}
			if got := first.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want %q: a kept copy is the stale answer this request replaces",
					got, "no-cache")
			}
			etag := first.Header().Get("ETag")
			if etag == "" {
				t.Fatal("the read carried no ETag, so a stored copy could not be revalidated")
			}

			reopen := getRevalidating(t, s, path, etag)
			if reopen.Code != http.StatusNotModified {
				t.Fatalf("repeating an unchanged read answered %d, want 304", reopen.Code)
			}
			if reopen.Body.Len() != 0 {
				t.Errorf("the 304 carried %d bytes of body", reopen.Body.Len())
			}
			for name, want := range map[string]string{"ETag": etag, "Cache-Control": "no-cache"} {
				if got := reopen.Header().Get(name); got != want {
					t.Errorf("the 304 repeated %s as %q, want %q", name, got, want)
				}
			}

			// A write that lands after the stored copy moves the digest, so the stored copy cannot be shown.
			tc.write(t, s, projID, taskID)
			moved := getRevalidating(t, s, path, etag)
			if moved.Code == http.StatusNotModified {
				t.Fatal("the read still answered 304 after a write landed")
			}
			if moved.Header().Get("ETag") == etag {
				t.Error("the digest did not move when the resource did")
			}

			if tc.other != "" {
				if other := getRevalidating(t, s, path+tc.other, "").Header().Get("ETag"); other == etag {
					t.Errorf("%s shares the whole read's digest", tc.other)
				}
			}
		})
	}

	// A response holding provider keys is not worth storing anywhere.
	s := testServer(t)
	if got := getRevalidating(t, s, "/api/config", "").Header().Get("Cache-Control"); got != "" {
		t.Errorf("GET /api/config declares Cache-Control %q", got)
	}
}

func getRevalidating(t *testing.T, s *Server, url, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodGet, url, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	return testResponseAuth(t, s, req)
}

// A floor bounds an older page, so the route refuses it without `before`.
func TestMessagesRouteRefusesAFloorAloneOrUnissued(t *testing.T) {
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
