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
		return decodeJSON(w, r, &v, limit), w
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

// TestMaxRequestBodyBytesCoversTheLargestRealPayload keeps the cap honest: the
// biggest thing the UI legitimately sends is a user message with four inline images,
// so raising or lowering the constant is a decision about that flow.
func TestMaxRequestBodyBytesCoversTheLargestRealPayload(t *testing.T) {
	const (
		maxImages     = 4               // frontend/src/lib/utils/attachments.ts MAX_IMAGES
		maxImageBytes = 4 * 1024 * 1024 // ... and MAX_IMAGE_SIZE
		base64Slack   = 4               // base64 is 4/3 of the bytes
	)
	largest := int64(maxImages*maxImageBytes)*int64(base64Slack)/3 + (1 << 20) // + prompt and framing
	if MaxRequestBodyBytes < largest {
		t.Errorf("MaxRequestBodyBytes = %d, but a message with %d images of %d bytes needs ~%d",
			MaxRequestBodyBytes, maxImages, maxImageBytes, largest)
	}
	if MaxRequestBodyBytes > 64<<20 {
		t.Errorf("MaxRequestBodyBytes = %d is looser than any flow needs; the cap is the point", MaxRequestBodyBytes)
	}
}
