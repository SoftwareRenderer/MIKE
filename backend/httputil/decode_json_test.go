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

// TestMaxMessageBodyBytesCoversTheLargestRealPayload keeps the cap honest: the biggest thing the UI sends is
// a user message with four images at the frontend's intake cap, so either constant can break that flow.
func TestMaxMessageBodyBytesCoversTheLargestRealPayload(t *testing.T) {
	const (
		maxImages     = 4                // frontend/src/lib/utils/attachments.ts MAX_IMAGES
		maxImageBytes = 20 * 1024 * 1024 // ... and MAX_IMAGE_SIZE, which mirrors exec.MaxUploadBytes
		base64Slack   = 4                // base64 is 4/3 of the bytes
	)
	largest := int64(maxImages*maxImageBytes)*int64(base64Slack)/3 + (1 << 20) // + prompt and framing
	if MaxMessageBodyBytes < largest {
		t.Errorf("MaxMessageBodyBytes = %d, but a message with %d images of %d bytes needs ~%d",
			MaxMessageBodyBytes, maxImages, maxImageBytes, largest)
	}
	if MaxMessageBodyBytes > 160<<20 {
		t.Errorf("MaxMessageBodyBytes = %d is looser than any flow needs; the cap is the point", MaxMessageBodyBytes)
	}
}

// TestGeneralJSONBoundStaysTighterThanTheMessageBound keeps the widening to the routes that need it: only
// the two message routes decode at MaxMessageBodyBytes.
func TestGeneralJSONBoundStaysTighterThanTheMessageBound(t *testing.T) {
	if MaxRequestBodyBytes > 32<<20 {
		t.Errorf("MaxRequestBodyBytes = %d: nothing without an attachment needs this much", MaxRequestBodyBytes)
	}
	if MaxRequestBodyBytes >= MaxMessageBodyBytes {
		t.Errorf("MaxRequestBodyBytes = %d is no tighter than the message bound %d, so widening one widened both",
			MaxRequestBodyBytes, MaxMessageBodyBytes)
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
