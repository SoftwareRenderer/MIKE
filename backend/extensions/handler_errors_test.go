package extensions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The extension endpoints have the same contract as the git and task ones: an
// extension that is absent is a 404, while a store that cannot answer is a 500.
// Before the taxonomy both looked identical to the browser, so a database fault
// rendered Settings → Extensions as "nothing installed".

func extErrorBody(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %q (%v)", rr.Body.String(), err)
	}
	return body["error"]
}

func TestHandleGet_UnknownExtensionIs404(t *testing.T) {
	h := testHandler(t)
	rr := httptest.NewRecorder()

	h.HandleGet(rr, "nonexistent")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := extErrorBody(t, rr); got != "extension not found" {
		t.Errorf("error = %q, want %q", got, "extension not found")
	}
}

func TestHandleGet_StoreFaultIs500NotPhantom404(t *testing.T) {
	h, extStore := testHandlerWithStore(t)

	if _, err := extStore.db.Exec(`DROP TABLE extensions`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rr := httptest.NewRecorder()
	h.HandleGet(rr, "any-id")

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("a database fault answered %d (body %q); want 500 — the bug this test exists for",
			rr.Code, rr.Body.String())
	}
	if got := extErrorBody(t, rr); got != "internal error" {
		t.Errorf("error = %q, want %q", got, "internal error")
	}
	if strings.Contains(rr.Body.String(), "no such table") {
		t.Errorf("response leaked database internals: %s", rr.Body.String())
	}
}
