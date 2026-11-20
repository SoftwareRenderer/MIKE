package httputil

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// The status and the body are one answer, and the encoder decides whether there is a body.
// docs/api-contract.md → The JSON era this server speaks.
func TestJSONResponseNeverAnswersWithAStatusTheBodyCannotMatch(t *testing.T) {
	logged, restore := logging.TestSink()
	defer restore()

	// "café" in Latin-1: the byte at index 3 is not a UTF-8 continuation byte.
	latin1 := map[string]string{"content": "caf\xE9"}

	rec := httptest.NewRecorder()
	JSONResponse(rec, http.StatusOK, latin1)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d: a value that cannot be encoded describes no body, so 200 is a lie",
			rec.Code, http.StatusInternalServerError)
	}
	if rec.Body.Len() == 0 {
		t.Error("the answer carried no body at all")
	}
	if got := decodeError(t, rec.Body.Bytes()); got != "internal error" {
		t.Errorf("error = %q, want MIKE's own sentence", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json: a client that parsed the request parses the answer too", ct)
	}
	if !strings.Contains(logged.String(), "invalid UTF-8") {
		t.Errorf("the cause belongs in the log; log was:\n%s", logged.String())
	}
	for _, leak := range []string{"caf", "\xE9"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the refused bytes reached the browser: %q", rec.Body.String())
		}
	}
}

// Buffering must not cost a good response its Content-Length, the only thing that names a body a HEAD
// request never sends. The status and the {"error": …} envelope of an encodable value are `Fail`'s cases in
// `httputil_test.go`.
func TestJSONResponseKeepsTheShapeOfWhatItCanEncode(t *testing.T) {
	rec := httptest.NewRecorder()
	JSONResponse(rec, http.StatusCreated, map[string]string{"content": "caf\u00E9"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got, want := rec.Body.String(), `{"content":"café"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length = %q, want the body's own length %d", cl, rec.Body.Len())
	}
}
