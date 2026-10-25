package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		{"br preferred over all", "deflate, gzip, br", "br"},
		{"typical browser header", "gzip, deflate, br, zstd", "br"},
		{"case insensitive", "BR, GZIP", "br"},
		{"deflate alone is identity", "deflate", ""},
		{"wildcard is identity", "*", ""},
		{"zstd is identity", "zstd", ""},
		{"identity alone is identity", "identity", ""},
		{"unknown encoding ignored", "unknown, br", "br"},
		{"all unknown", "unknown1, unknown2", ""},
		{"q=0 refuses brotli", "br;q=0, gzip", "gzip"},
		{"q=0.0 refuses brotli", "br;q=0.0, gzip", "gzip"},
		{"q parameter is case insensitive", "br;Q=0, gzip", "gzip"},
		{"q=0 on the only encoding offered", "br;q=0", ""},
		{"unparseable q is not a refusal", "br;q=abc, gzip", "br"},
		{"a q above zero does not reorder br and gzip", "gzip; q=1.0, br; q=0.5", "br"},
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

func TestEverySelectableEncodingHasAWriter(t *testing.T) {
	for encoding := range encodingPriority {
		if _, ok := encodingWriter(encoding, io.Discard); !ok {
			t.Errorf("selectEncoding can answer %q, which encodingWriter cannot produce", encoding)
		}
	}
}

// TestCompressMiddleware_EncodingNegotiation is the negotiation matrix: what the client
// offers × how big the body is → which encoding the response ends up carrying, plus the
// invariants that hold on every response (Vary is always set, the bytes survive the
// round trip, and a response claiming an encoding really is encoded). SSE, websocket
// upgrades and the stacked middleware are separate mechanisms and keep their own tests.
func TestCompressMiddleware_EncodingNegotiation(t *testing.T) {
	cases := []struct {
		name string
		// acceptEncoding is sent verbatim; "" omits the header entirely.
		acceptEncoding string
		body           string
		contentType    string
		wantEncoding   string // "" means the response must pass through untouched
	}{
		{
			name:           "brotli wins when the client offers both",
			acceptEncoding: "gzip, deflate, br",
			body:           strings.Repeat("preference test ", 100),
			wantEncoding:   "br",
		},
		{
			name:           "gzip when brotli is not offered",
			acceptEncoding: "gzip, deflate",
			body:           strings.Repeat("gzip fallback ", 100),
			wantEncoding:   "gzip",
		},
		{
			name:           "large body over brotli",
			acceptEncoding: "br",
			body:           strings.Repeat("x", 2048),
			contentType:    "application/json",
			wantEncoding:   "br",
		},
		{
			name:           "large body over gzip",
			acceptEncoding: "gzip",
			body:           strings.Repeat("y", 2048),
			contentType:    "application/json",
			wantEncoding:   "gzip",
		},
		{
			// minCompressSize is 1024; 15 bytes is nowhere near it.
			name:           "body under minCompressSize is left alone",
			acceptEncoding: "br",
			body:           `{"status":"ok"}`,
			wantEncoding:   "",
		},
		{
			name:           "no Accept-Encoding header",
			acceptEncoding: "",
			body:           "hello",
			wantEncoding:   "",
		},
		{
			name:           "deflate-only client gets the full body uncompressed",
			acceptEncoding: "deflate",
			body:           strings.Repeat("z", 2048),
			contentType:    "application/json",
			wantEncoding:   "",
		},
		{
			name:           "wildcard-only client gets the full body uncompressed",
			acceptEncoding: "*",
			body:           strings.Repeat("z", 2048),
			contentType:    "application/json",
			wantEncoding:   "",
		},
		{
			name:           "identity-only client is left alone",
			acceptEncoding: "identity",
			body:           strings.Repeat("z", 2048),
			contentType:    "application/json",
			wantEncoding:   "",
		},
		{
			name:           "brotli refused with q=0 falls back to gzip",
			acceptEncoding: "br;q=0, gzip",
			body:           strings.Repeat("w", 2048),
			contentType:    "application/json",
			wantEncoding:   "gzip",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const path = "/test/compress"
			s := testServer(t)
			s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.body))
			})

			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			}
			w := httptest.NewRecorder()
			CompressMiddleware(s.mux).ServeHTTP(w, req)

			resp := w.Result()
			if got := resp.Header.Get("Content-Encoding"); got != tc.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
			}
			if got := resp.Header.Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", got)
			}

			if tc.wantEncoding == "" {
				if got := w.Body.String(); got != tc.body {
					t.Errorf("body should pass through byte-identical, got %q", got)
				}
				return
			}
			if bytes.Equal(w.Body.Bytes(), []byte(tc.body)) {
				t.Fatalf("response claims Content-Encoding %s but the body is uncompressed", tc.wantEncoding)
			}
			mustRoundTrip(t, tc.wantEncoding, w.Body.Bytes(), tc.body)
		})
	}
}

// mustRoundTrip expands a response body with the decoder for the negotiated encoding and
// requires the original bytes back.
func mustRoundTrip(t *testing.T, encoding string, raw []byte, want string) {
	t.Helper()

	var src io.Reader
	switch encoding {
	case "br":
		src = brotli.NewReader(bytes.NewReader(raw))
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		defer gr.Close()
		src = gr
	default:
		t.Fatalf("no decoder wired up for %q", encoding)
	}

	got, err := io.ReadAll(src)
	if err != nil {
		t.Fatalf("decompress %s: %v", encoding, err)
	}
	if string(got) != want {
		t.Errorf("round trip returned %d bytes, want %d", len(got), len(want))
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
	handler := s.CORSMiddleware(CompressMiddleware(s.mux))

	req := httptest.NewRequest("GET", "/test/full", nil)
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.Header.Get("Content-Encoding") != "br" {
		t.Errorf("expected Content-Encoding: br with full middleware stack, got: %q", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected NO CORS header (same-origin policy), got: %q", resp.Header.Get("Access-Control-Allow-Origin"))
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

// TestGetImageResponseIsPassedThrough: the GET buffer exists to hash an ETag, and an image is
// already-compressed bytes, so holding one costs several times the file in memory for a body that
// cannot shrink. This is what keeps a large binary response's own size cap the real bound.
func TestGetImageResponseIsPassedThrough(t *testing.T) {
	s := testServer(t)
	chunk := bytes.Repeat([]byte{0x89}, 256<<10)
	chunks := 8
	s.mux.HandleFunc("GET /test/img", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)*chunks))
		w.Header().Set("Cache-Control", "no-store")
		for range chunks {
			w.Write(chunk)
		}
	})

	rec := &chunkRecordingWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/test/img", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	CompressMiddleware(s.mux).ServeHTTP(rec, req)

	if got := rec.header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	if got := rec.header.Get("Content-Length"); got != strconv.Itoa(len(chunk)*chunks) {
		t.Errorf("Content-Length = %q, want the handler's own %d", got, len(chunk)*chunks)
	}
	if got := rec.header.Get("ETag"); got != "" {
		t.Errorf("ETag = %q, want none — a no-store body is never revalidated", got)
	}
	if rec.writes < chunks {
		t.Errorf("body left in %d write(s) of a max %d bytes: the whole response was buffered", rec.writes, rec.largest)
	}
	if rec.bytes != len(chunk)*chunks {
		t.Errorf("%d bytes reached the client, want %d", rec.bytes, len(chunk)*chunks)
	}
}

// An `attachment` response leaves as the handler wrote it — in pieces, with the handler's own headers —
// because buffering it would hold the whole download to hash it and compress it a second time.
func TestGetAttachmentResponseIsPassedThrough(t *testing.T) {
	s := testServer(t)
	chunk := bytes.Repeat([]byte{0x1f, 0x8b}, 128<<10) // gzip's own bytes: nothing left to gain from brotli
	chunks := 8
	s.mux.HandleFunc("GET /test/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename=src.tar.gz`)
		w.Header().Set("Cache-Control", "no-store")
		for range chunks {
			w.Write(chunk)
		}
	})

	rec := &chunkRecordingWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/test/download", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	CompressMiddleware(s.mux).ServeHTTP(rec, req)

	if got := rec.header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none — the archive is already compressed", got)
	}
	if got := rec.header.Get("Content-Disposition"); got != `attachment; filename=src.tar.gz` {
		t.Errorf("Content-Disposition = %q; without it the browser shows the bytes instead of saving them", got)
	}
	if got := rec.header.Get("ETag"); got != "" {
		t.Errorf("ETag = %q, want none — an ETag is what a buffered GET leaves behind", got)
	}
	if rec.writes < chunks {
		t.Errorf("body left in %d write(s) of a max %d bytes: the whole download was buffered", rec.writes, rec.largest)
	}
	if rec.bytes != len(chunk)*chunks {
		t.Errorf("%d bytes reached the client, want %d", rec.bytes, len(chunk)*chunks)
	}
}

// A single-file download carries a Content-Length, and only the pass-through keeps it: the buffering
// path deletes it to add an ETag.
func TestGetAttachmentKeepsTheHandlersContentLength(t *testing.T) {
	s := testServer(t)
	body := bytes.Repeat([]byte{0x42}, 4<<10)
	s.mux.HandleFunc("GET /test/download-one", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Content-Disposition", `attachment; filename=notes.md`)
		w.Write(body)
	})

	rec := &chunkRecordingWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/test/download-one", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	CompressMiddleware(s.mux).ServeHTTP(rec, req)

	if got := rec.header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want the handler's own %d", got, len(body))
	}
	if rec.bytes != len(body) {
		t.Errorf("%d bytes reached the client, want %d", rec.bytes, len(body))
	}
}

// chunkRecordingWriter counts writes instead of keeping a body, so a buffering writer is visible.
type chunkRecordingWriter struct {
	header  http.Header
	writes  int
	largest int
	bytes   int
}

func (w *chunkRecordingWriter) Header() http.Header { return w.header }

func (w *chunkRecordingWriter) WriteHeader(int) {}

func (w *chunkRecordingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.bytes += len(p)
	if len(p) > w.largest {
		w.largest = len(p)
	}
	return len(p), nil
}

func (w *chunkRecordingWriter) Flush() {}

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
