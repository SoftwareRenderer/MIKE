package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The returning window's one request: cheap enough to make on every return, and answerable with no body when
// it says nothing moved. `docs/reconnection.md → The tab that comes back`.

type syncDigest struct {
	Conversation struct {
		Epoch    string `json:"epoch"`
		Revision uint64 `json:"revision"`
		Messages int    `json:"messages"`
	} `json:"conversation"`
}

func readSync(t *testing.T, s *Server, path, ifNoneMatch string) (syncDigest, http.Header, int) {
	t.Helper()
	req := testRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rr := testResponseAuth(t, s, req)
	var got syncDigest
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode digest: %v (%s)", err, rr.Body.String())
		}
	}
	return got, rr.Header(), rr.Code
}

// TestSyncRouteIsAnsweredWithARevalidatableDigest: the digest is asked on every return, so it must not be
// kept, and an unchanged answer must cost no body. The ETag is CompressMiddleware's own.
func TestSyncRouteIsAnsweredWithARevalidatableDigest(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Sync")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/sync"

	first, headers, code := readSync(t, s, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, code)
	}
	if headers.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache: a kept digest is the stale answer this request replaces",
			headers.Get("Cache-Control"))
	}
	etag := headers.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag: every return would pay for the whole body")
	}
	if first.Conversation.Epoch == "" || first.Conversation.Messages == 0 {
		t.Errorf("digest carries epoch %q and %d rows, want a named process and the task's own prompt row",
			first.Conversation.Epoch, first.Conversation.Messages)
	}

	_, reheaders, code := readSync(t, s, path, etag)
	if code != http.StatusNotModified {
		t.Fatalf("revalidating an unchanged digest answered %d, want 304", code)
	}
	if reheaders.Get("Cache-Control") != "no-cache" {
		t.Errorf("the 304 dropped Cache-Control (%q), so a proxy may keep the next answer too", reheaders.Get("Cache-Control"))
	}
}

// TestSyncDigestMovesWithADurableWrite: a write that lands must move the revision, and with it the ETag.
func TestSyncDigestMovesWithADurableWrite(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Sync")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/sync"

	before, _, _ := readSync(t, s, path, "")
	s.appStreamMgr.DurableThrough(projID, taskID)
	after, headers, code := readSync(t, s, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, code)
	}
	if after.Conversation.Revision != before.Conversation.Revision+1 {
		t.Fatalf("revision is %d after a durable write, want %d", after.Conversation.Revision, before.Conversation.Revision+1)
	}
	if _, _, code := readSync(t, s, path, headers.Get("ETag")); code != http.StatusNotModified {
		t.Fatal("the new digest does not revalidate against its own ETag")
	}
}

// TestSyncRouteIsNotTokenScope: the digest is a task read, but it is not one a headless client was ever
// granted, and the approval test holds that list.
func TestSyncRouteIsAnsweredWithoutSessionByNothing(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Sync")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/sync"

	rr := testResponse(t, s, testRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated digest answered %d, want 401: %s", rr.Code, rr.Body.String())
	}
}
