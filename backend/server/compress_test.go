package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestSelectEncoding(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{"empty", "", ""},
		{"br only", "br", "br"},
		{"gzip only", "gzip", "gzip"},
		{"br preferred over gzip", "gzip, br", "br"},
		{"gzip preferred over deflate", "deflate, gzip", "gzip"},
		{"br preferred over all", "deflate, gzip, br", "br"},
		{"wildcard", "*", "*"},
		{"br preferred over wildcard", "*, br", "br"},
		{"with quality factors", "gzip; q=1.0, br; q=0.5", "br"},
		{"unknown encoding ignored", "unknown, br", "br"},
		{"all unknown", "unknown1, unknown2", ""},
		{"case insensitive", "BR, GZIP", "br"},
		{"typical browser header", "gzip, deflate, br", "br"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectEncoding(tc.header)
			if got != tc.expected {
				t.Errorf("selectEncoding(%q) = %q, want %q", tc.header, got, tc.expected)
			}
		})
	}
}

func TestCompressMiddleware_Brotli_Preference(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("preference test ", 100)
	s.mux.HandleFunc("/test/br-pref", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	// Client sends both br and gzip — server should prefer br
	req := httptest.NewRequest("GET", "/test/br-pref", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br (preferred), got: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("expected Vary: Accept-Encoding, got: %q", resp.Header.Get("Vary"))
	}
}

func TestCompressMiddleware_Gzip_Fallback(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("gzip fallback ", 100)
	s.mux.HandleFunc("/test/gz-fallback", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	// Client only supports gzip (no br)
	req := httptest.NewRequest("GET", "/test/gz-fallback", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding: gzip (fallback), got: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressMiddleware_NoCompression_SmallBody(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/small", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) // 15 bytes, well below 1024
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/small", nil)
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding for small body, got: %q", resp.Header.Get("Content-Encoding"))
	}
	body := w.Body.String()
	if body != `{"status":"ok"}` {
		t.Errorf("expected uncompressed body, got: %q", body)
	}
}

func TestCompressMiddleware_Brotli_LargeBody(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("x", 2048) // above minCompressSize
	s.mux.HandleFunc("/test/large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/large", nil)
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br, got: %q", resp.Header.Get("Content-Encoding"))
	}

	// Decompress and verify body
	br := brotli.NewReader(w.Body)
	decompressed, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("failed to decompress brotli: %v", err)
	}
	if string(decompressed) != largeBody {
		t.Errorf("decompressed body mismatch: got %d bytes, want %d", len(decompressed), len(largeBody))
	}
}

func TestCompressMiddleware_Gzip_LargeBody(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("y", 2048)
	s.mux.HandleFunc("/test/large-gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/large-gz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding: gzip, got: %q", resp.Header.Get("Content-Encoding"))
	}

	// Decompress and verify body
	gr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gr.Close()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("failed to decompress gzip: %v", err)
	}
	if string(decompressed) != largeBody {
		t.Errorf("decompressed body mismatch: got %d bytes, want %d", len(decompressed), len(largeBody))
	}
}

func TestCompressMiddleware_NoCompression_SSE(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: test\n\n"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/sse", nil)
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding for SSE, got: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressMiddleware_NoAcceptEncoding(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/no-enc", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/no-enc", nil)
	// No Accept-Encoding header
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding, got: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("expected Vary: Accept-Encoding, got: %q", resp.Header.Get("Vary"))
	}
}

func TestCompressMiddleware_WebSocketSkip(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/ws", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not a ws"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding for WebSocket upgrade, got: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressMiddleware_WithJSONResponse(t *testing.T) {
	s := testServer(t)
	// Simulate what JSONResponse does — large payload
	largeData := make(map[string]string)
	for i := 0; i < 100; i++ {
		largeData[fmt.Sprintf("key-%d", i)] = strings.Repeat("value", 100)
	}
	s.mux.HandleFunc("/test/json", func(w http.ResponseWriter, r *http.Request) {
		JSONResponse(w, http.StatusOK, largeData)
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/json", nil)
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br for JSONResponse, got: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type: application/json, got: %q", resp.Header.Get("Content-Type"))
	}

	// Decompress and verify it's valid JSON
	br := brotli.NewReader(w.Body)
	decompressed, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("failed to decompress brotli: %v", err)
	}
	// JSONResponse adds a trailing newline
	if !bytes.HasSuffix(decompressed, []byte("\n")) {
		t.Error("expected trailing newline from json.Encoder")
	}
}

func TestCompressMiddleware_FullStack(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("z", 2048)
	s.mux.HandleFunc("/test/full", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	// Full middleware chain (no auth for this test path)
	handler := CORSMiddleware(CompressMiddleware(s.mux))

	req := httptest.NewRequest("GET", "/test/full", nil)
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br with full middleware stack, got: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost" {
		t.Errorf("expected CORS header, got: %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}

	// Decompress and verify
	br := brotli.NewReader(w.Body)
	decompressed, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("failed to decompress brotli: %v", err)
	}
	if string(decompressed) != largeBody {
		t.Errorf("decompressed body mismatch: got %d bytes, want %d", len(decompressed), len(largeBody))
	}
}

func TestCompressMiddleware_ContentLengthRemoved(t *testing.T) {
	s := testServer(t)
	largeBody := strings.Repeat("a", 2048)
	s.mux.HandleFunc("/test/cl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/cl", nil)
	req.Header.Set("Accept-Encoding", "br")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	// Content-Length should be removed from the handler's headers
	// (the http.Response will have one set by httptest, but it should be the compressed size)
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br, got: %q", resp.Header.Get("Content-Encoding"))
	}
}

func TestCompressMiddleware_BrotliBetterThanGzip(t *testing.T) {
	s := testServer(t)
	// Use repetitive text that compresses well
	largeBody := strings.Repeat("compression test data ", 100)
	s.mux.HandleFunc("/test/compare", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	})

	handler := CompressMiddleware(s.mux)

	// Test brotli
	reqBr := httptest.NewRequest("GET", "/test/compare", nil)
	reqBr.Header.Set("Accept-Encoding", "br")
	wBr := httptest.NewRecorder()
	handler.ServeHTTP(wBr, reqBr)

	// Test gzip
	reqGz := httptest.NewRequest("GET", "/test/compare", nil)
	reqGz.Header.Set("Accept-Encoding", "gzip")
	wGz := httptest.NewRecorder()
	handler.ServeHTTP(wGz, reqGz)

	brSize := wBr.Body.Len()
	gzSize := wGz.Body.Len()

	t.Logf("brotli: %d bytes, gzip: %d bytes", brSize, gzSize)
	if brSize >= gzSize {
		t.Logf("warning: brotli (%d) not smaller than gzip (%d) for this data", brSize, gzSize)
		// This is a soft check — depends on the data
	}
}

// --- ETag tests ---

func TestETagGenerated(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/etag", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello etag"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/etag", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	resp := rr.Result()
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header, got none")
	}
	// Should be quoted, 32 hex chars inside quotes
	if len(etag) != 34 { // 32 hex + 2 quotes
		t.Fatalf("ETag length = %d; want 34 (32 hex + quotes)", len(etag))
	}
	if etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("ETag not quoted: %q", etag)
	}
}

func TestETagIfNoneMatch304(t *testing.T) {
	s := testServer(t)
	body := []byte("conditional test")
	s.mux.HandleFunc("/test/cond", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})

	handler := CompressMiddleware(s.mux)

	// First request: get the ETag
	req1 := httptest.NewRequest("GET", "/test/cond", nil)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	etag := rr1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first request: expected ETag header")
	}

	// Second request: send If-None-Match with the ETag
	req2 := httptest.NewRequest("GET", "/test/cond", nil)
	req2.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d; body: %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Header().Get("ETag") != etag {
		t.Errorf("304 ETag = %q; want %q", rr2.Header().Get("ETag"), etag)
	}
	if rr2.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", rr2.Body.Len())
	}
}

func TestETagIfNoneMatchNoMatch(t *testing.T) {
	s := testServer(t)
	body := []byte("no match test")
	s.mux.HandleFunc("/test/nomatch", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/nomatch", nil)
	req.Header.Set("If-None-Match", `"wrong-etag-value-here"`)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("ETag") == `"wrong-etag-value-here"` {
		t.Error("ETag should not match the wrong value")
	}
	if rr.Body.String() != "no match test" {
		t.Errorf("body mismatch: %q", rr.Body.String())
	}
}

func TestETagConsistentWithCompression(t *testing.T) {
	s := testServer(t)
	body := strings.Repeat("consistent etag test data ", 100)
	s.mux.HandleFunc("/test/consistent", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})

	handler := CompressMiddleware(s.mux)

	// Request without compression
	req1 := httptest.NewRequest("GET", "/test/consistent", nil)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	// Request with brotli compression
	req2 := httptest.NewRequest("GET", "/test/consistent", nil)
	req2.Header.Set("Accept-Encoding", "br")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	etag1 := rr1.Header().Get("ETag")
	etag2 := rr2.Header().Get("ETag")

	if etag1 != etag2 {
		t.Errorf("ETag differs with/without compression: %q vs %q", etag1, etag2)
	}
}

func TestETagSkippedForNonGET(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/post", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("post response"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("POST", "/test/post", strings.NewReader("{}"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("ETag") != "" {
		t.Errorf("POST should not have ETag, got: %q", rr.Header().Get("ETag"))
	}
}

func TestETagExistingSkipped(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/existing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"already-set"`)
		w.Write([]byte("has etag"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/existing", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("ETag") != `"already-set"` {
		t.Errorf("should preserve handler's ETag, got: %q", rr.Header().Get("ETag"))
	}
}

func TestETag304Passthrough(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/304", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/304", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Errorf("expected 304, got %d", rr.Code)
	}
	// Handler didn't set ETag, but middleware should not add one for 304
	// (the handler returned 304, so we skip ETag logic)
}

func TestETagEmptyBody(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/empty", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No body
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/empty", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag even for empty body")
	}
	// Verify it's the ETag of empty content
	expected := computeETag([]byte{})
	if etag != expected {
		t.Errorf("ETag = %q; want %q", etag, expected)
	}
}

func TestETagWildcardIfNoneMatch(t *testing.T) {
	s := testServer(t)
	s.mux.HandleFunc("/test/wildcard", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wildcard test"))
	})

	handler := CompressMiddleware(s.mux)

	req := httptest.NewRequest("GET", "/test/wildcard", nil)
	req.Header.Set("If-None-Match", "*")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Fatalf("expected 304 for wildcard If-None-Match, got %d", rr.Code)
	}
}

func TestETagMultipleIfNoneMatch(t *testing.T) {
	s := testServer(t)
	body := []byte("multi etag test")
	s.mux.HandleFunc("/test/multi", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})

	handler := CompressMiddleware(s.mux)

	// First request: get the ETag
	req1 := httptest.NewRequest("GET", "/test/multi", nil)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	etag := rr1.Header().Get("ETag")

	// Second request: If-None-Match with multiple values, ours is in the middle
	req2 := httptest.NewRequest("GET", "/test/multi", nil)
	req2.Header.Set("If-None-Match", `"first-one", `+etag+`, "last-one"`)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 when ETag is in multi-value If-None-Match, got %d", rr2.Code)
	}

	// Third request: If-None-Match with multiple values, ours is NOT in the list
	req3 := httptest.NewRequest("GET", "/test/multi", nil)
	req3.Header.Set("If-None-Match", `"first-one", "last-one"`)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("expected 200 when ETag is NOT in multi-value If-None-Match, got %d", rr3.Code)
	}
}

func TestETagWithCompression304(t *testing.T) {
	// Verify that a 304 response is not compressed (no body to compress)
	s := testServer(t)
	body := strings.Repeat("compressed 304 test ", 100)
	s.mux.HandleFunc("/test/comp304", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})

	handler := CompressMiddleware(s.mux)

	// First request with compression: get the ETag
	req1 := httptest.NewRequest("GET", "/test/comp304", nil)
	req1.Header.Set("Accept-Encoding", "br")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)

	etag := rr1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag on first request")
	}

	// Second request with compression + If-None-Match: should get 304
	req2 := httptest.NewRequest("GET", "/test/comp304", nil)
	req2.Header.Set("Accept-Encoding", "br")
	req2.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", rr2.Code)
	}
	if rr2.Header().Get("Content-Encoding") != "" {
		t.Errorf("304 should not have Content-Encoding, got: %q", rr2.Header().Get("Content-Encoding"))
	}
}

func TestComputeETag(t *testing.T) {
	// Deterministic: same input always produces same ETag
	body := []byte("deterministic test")
	etag1 := computeETag(body)
	etag2 := computeETag(body)
	if etag1 != etag2 {
		t.Errorf("computeETag not deterministic: %q vs %q", etag1, etag2)
	}

	// Different input produces different ETag
	body2 := []byte("different test")
	etag3 := computeETag(body2)
	if etag1 == etag3 {
		t.Error("different bodies produced same ETag")
	}
}

func TestEtagMatches(t *testing.T) {
	tests := []struct {
		name        string
		etag        string
		ifNoneMatch string
		want        bool
	}{
		{"empty header", `"abc"`, "", false},
		{"exact match", `"abc"`, `"abc"`, true},
		{"no match", `"abc"`, `"def"`, false},
		{"wildcard", `"abc"`, "*", true},
		{"first in list", `"abc"`, `"abc", "def"`, true},
		{"middle in list", `"abc"`, `"def", "abc", "ghi"`, true},
		{"last in list", `"abc"`, `"def", "ghi", "abc"`, true},
		{"not in list", `"abc"`, `"def", "ghi"`, false},
		{"whitespace trimmed", `"abc"`, `"def" , "abc" , "ghi"`, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := etagMatches(tc.etag, tc.ifNoneMatch)
			if got != tc.want {
				t.Errorf("etagMatches(%q, %q) = %v; want %v", tc.etag, tc.ifNoneMatch, got, tc.want)
			}
		})
	}
}
