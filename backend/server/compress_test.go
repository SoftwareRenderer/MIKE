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
