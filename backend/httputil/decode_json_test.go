package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeJSONBoundsTheBody pins the two different failures a handler's client sees:
// a malformed body is a 400, an oversized one is a 413. Answering "invalid request
// body" to a body that was simply too big tells the client to retry with the same
// bytes forever; and without any bound at all, one authenticated request could make
// the server allocate until it dies.
func TestDecodeJSONBoundsTheBody(t *testing.T) {
	const limit = 1 << 10

	post := func(body string) (bool, *httptest.ResponseRecorder) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/anything", strings.NewReader(body))
		w := httptest.NewRecorder()
		var v map[string]any
		return decodeJSON(w, r, &v, limit, false), w
	}

	if ok, w := post(`{"a":1}`); !ok || w.Body.Len() != 0 {
		t.Errorf("a valid body: ok=%v, wrote %q", ok, w.Body.String())
	}
	if ok, w := post(`{"a":`); ok || w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: ok=%v code=%d, want false and %d (%s)", ok, w.Code, http.StatusBadRequest, w.Body.String())
	}
	ok, w := post(`{"a":"` + strings.Repeat("x", limit*4) + `"}`)
	if ok || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: got %d, want %d (%s)", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "too large") {
		t.Errorf("the 413 should say why: %s", w.Body.String())
	}
}

// The message ceiling is sized by the file limit it carries attachments at, so this keeps the derivation
// honest in both directions: the biggest thing the UI sends fits, and the number stays a bound.
func TestMaxMessageBodyBytesCoversTheLargestRealPayload(t *testing.T) {
	const (
		maxImages   = 4                // frontend/src/lib/utils/attachments.ts MAX_IMAGES
		fileLimit   = 20 * 1024 * 1024 // startup.DefaultFileSizeLimitMB, the browser's intake default
		base64Slack = 4                // base64 is 4/3 of the bytes
	)
	largest := int64(maxImages*fileLimit)*int64(base64Slack)/3 + (1 << 20) // + prompt and framing
	if ceiling := MessageBodyBytes(fileLimit); ceiling < largest {
		t.Errorf("MessageBodyBytes(%d) = %d, but a message with %d images of %d bytes needs ~%d",
			fileLimit, ceiling, maxImages, fileLimit, largest)
	}
	if ceiling := MessageBodyBytes(fileLimit); ceiling > 160<<20 {
		t.Errorf("MessageBodyBytes(%d) = %d is looser than any flow needs; the cap is the point", fileLimit, ceiling)
	}
	if MessageBodyBytes(2*fileLimit) <= MessageBodyBytes(fileLimit) {
		t.Error("doubling the file limit left the message ceiling behind — the drift the derivation exists to prevent")
	}
	if MessageBodyBytes(1<<20) <= MaxRequestBodyBytes {
		t.Errorf("MessageBodyBytes(1MB) = %d is no wider than the general bound %d",
			MessageBodyBytes(1<<20), MaxRequestBodyBytes)
	}
}

// TestGeneralJSONBoundStaysTighterThanTheMessageBound keeps the widening to the routes that need it: the
// general bound is the tighter of the pair at every limit the ladder can resolve.
func TestGeneralJSONBoundStaysTighterThanTheMessageBound(t *testing.T) {
	if MaxRequestBodyBytes > 32<<20 {
		t.Errorf("MaxRequestBodyBytes = %d: nothing without an attachment needs this much", MaxRequestBodyBytes)
	}
	for _, mb := range []int64{1, 20, 100, 4096} {
		if ceiling := MessageBodyBytes(mb << 20); MaxRequestBodyBytes >= ceiling {
			t.Errorf("MaxRequestBodyBytes = %d is no tighter than the message bound %d at a %dMB limit, so widening one widened both",
				MaxRequestBodyBytes, ceiling, mb)
		}
	}
}

// TestDecodeJSONOrEmptyTakesAnAbsentBodyForNoFields pins the difference between a request that sent
// nothing and one that sent something unreadable: the first carries no fields, the second is a 400.
func TestDecodeJSONOrEmptyTakesAnAbsentBodyForNoFields(t *testing.T) {
	post := func(body string) (bool, *httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/anything", strings.NewReader(body))
		w := httptest.NewRecorder()
		v := map[string]any{"name": "preset"}
		return DecodeJSONOrEmpty(w, r, &v), w, v
	}

	if ok, w, v := post(""); !ok || w.Body.Len() != 0 || v["name"] != "preset" {
		t.Errorf("an absent body: ok=%v code=%d fields=%v; want the handler's own values kept", ok, w.Code, v)
	}
	if ok, w, v := post(`{"name":"sent"}`); !ok || w.Body.Len() != 0 || v["name"] != "sent" {
		t.Errorf("a body that was sent: ok=%v code=%d fields=%v", ok, w.Code, v)
	}
	if ok, w, _ := post(`{"name":`); ok || w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: ok=%v code=%d, want false and %d", ok, w.Code, http.StatusBadRequest)
	}
}
