package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// minCompressSize is the minimum response size to bother with compression.
const minCompressSize = 1024

// etagLen is the number of hex characters in the truncated SHA-256 ETag.
const etagLen = 32

// computeETag computes a strong ETag from the given body.
// Returns a quoted SHA-256 hash truncated to etagLen hex characters.
func computeETag(body []byte) string {
	h := sha256.Sum256(body)
	return `"` + hex.EncodeToString(h[:])[:etagLen] + `"`
}

// etagMatches checks if the given ETag matches the If-None-Match header value.
// Handles comma-separated ETags and the wildcard "*".
func etagMatches(etag, ifNoneMatch string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimSpace(candidate) == etag {
			return true
		}
	}
	return false
}

// compressionLevel maps encoding name to its preference rank (lower = preferred).
var encodingPriority = map[string]int{
	"br":      1,
	"gzip":    2,
	"deflate": 3,
	"*":       4,
}

// selectEncoding picks the best compression encoding the client supports.
// Returns "" if no supported encoding is found.
func selectEncoding(acceptEncoding string) string {
	if acceptEncoding == "" {
		return ""
	}

	best := ""
	bestRank := 5 // worse than wildcard

	for part := range strings.SplitSeq(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		// Strip quality factor (e.g. "br;q=0.8" -> "br")
		encoding := strings.TrimSpace(strings.Split(part, ";")[0])

		lower := strings.ToLower(encoding)
		rank, ok := encodingPriority[lower]
		if !ok {
			continue
		}
		if rank < bestRank {
			bestRank = rank
			best = lower
		}
	}

	return best
}

// CompressMiddleware wraps an http.Handler to compress responses when the
// client supports it (brotli > gzip). Only compresses bodies >= minCompressSize.
// For GET requests, also computes strong ETags and handles If-None-Match → 304.
func CompressMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip for WebSocket upgrades (they hijack the connection).
		if strings.Contains(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}

		// For GET requests, always route through compressWriter so ETags are
		// computed even when the client sends no Accept-Encoding.
		if r.Method == http.MethodGet {
			encoding := selectEncoding(r.Header.Get("Accept-Encoding"))
			cw := newCompressWriter(w, r, encoding)
			next.ServeHTTP(cw, r)
			cw.finish()
			return
		}

		// Non-GET: only buffer when compression is requested.
		encoding := selectEncoding(r.Header.Get("Accept-Encoding"))
		if encoding == "" {
			w.Header().Set("Vary", "Accept-Encoding")
			next.ServeHTTP(w, r)
			return
		}

		cw := newCompressWriter(w, r, encoding)
		next.ServeHTTP(cw, r)
		cw.finish()
	})
}

type compressWriter struct {
	http.ResponseWriter
	headers       http.Header
	body          bytes.Buffer
	code          int
	wroteHeader   bool
	committed     bool
	encoding      string
	method        string
	req           *http.Request
	gz            *gzip.Writer
	br            *brotli.Writer
	contentLength int64 // parsed from Content-Length header, -1 if unknown
}

func newCompressWriter(w http.ResponseWriter, r *http.Request, encoding string) *compressWriter {
	return &compressWriter{
		ResponseWriter: w,
		headers:        make(http.Header),
		encoding:       encoding,
		method:         r.Method,
		req:            r,
		contentLength:  -1,
	}
}

func (c *compressWriter) Header() http.Header {
	return c.headers
}

func (c *compressWriter) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.code = code
	if cl := c.headers.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			c.contentLength = n
		}
	}
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.committed {
		switch c.encoding {
		case "br":
			if c.br != nil {
				return c.br.Write(p)
			}
		case "gzip":
			if c.gz != nil {
				return c.gz.Write(p)
			}
		}
		return c.ResponseWriter.Write(p)
	}

	// Bodies that must leave as the handler wrote them. An SSE stream needs real-time
	// flushes; an image is bytes the browser already compressed, so the GET buffer below
	// would hold several times the file to hash an ETag, then brotli it for nothing.
	if ct := c.headers.Get("Content-Type"); ct == "text/event-stream" || strings.HasPrefix(ct, "image/") {
		c.passThrough()
		return c.ResponseWriter.Write(p)
	}

	// For GET requests, always buffer the full body (needed for ETag computation).
	if c.method == http.MethodGet {
		return c.body.Write(p)
	}

	// Non-GET: buffer until large enough, then commit for streaming compression.
	n, err := c.body.Write(p)
	if err != nil {
		return n, err
	}

	estimated := int64(c.body.Len())
	if c.contentLength >= 0 && c.contentLength > estimated {
		estimated = c.contentLength
	}
	if estimated >= minCompressSize {
		c.commit()
	}
	return n, err
}

// passThrough sends the response exactly as the handler wrote it — headers and Content-Length
// included — and sends every later write straight to the socket.
func (c *compressWriter) passThrough() {
	c.committed = true
	for k, vv := range c.headers {
		for _, v := range vv {
			c.ResponseWriter.Header().Add(k, v)
		}
	}
	c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
	c.ResponseWriter.WriteHeader(c.code)
	if c.body.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.body.Bytes())
		c.body.Reset()
	}
}

func (c *compressWriter) commit() {
	c.committed = true
	c.headers.Set("Content-Encoding", c.encoding)
	c.headers.Del("Content-Length")

	for k, vv := range c.headers {
		for _, v := range vv {
			c.ResponseWriter.Header().Add(k, v)
		}
	}
	c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
	c.ResponseWriter.WriteHeader(c.code)

	switch c.encoding {
	case "br":
		c.br = brotli.NewWriter(c.ResponseWriter)
		c.br.Write(c.body.Bytes())
	case "gzip":
		c.gz = gzip.NewWriter(c.ResponseWriter)
		c.gz.Write(c.body.Bytes())
	}
}

func (c *compressWriter) finish() {
	// GET requests that haven't been committed (e.g. SSE streamed out) get ETag handling.
	if c.method == http.MethodGet && !c.committed {
		c.finishGET()
		return
	}

	if !c.committed {
		// Flush headers and write body uncompressed.
		for k, vv := range c.headers {
			for _, v := range vv {
				c.ResponseWriter.Header().Add(k, v)
			}
		}
		c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
		c.ResponseWriter.WriteHeader(c.code)
		c.ResponseWriter.Write(c.body.Bytes())
		return
	}

	switch c.encoding {
	case "br":
		if c.br != nil {
			c.br.Close()
		}
	case "gzip":
		if c.gz != nil {
			c.gz.Close()
		}
	}
}

// finishGET handles ETag computation and If-None-Match for GET responses.
// The complete uncompressed body is available in c.body.
func (c *compressWriter) finishGET() {
	body := c.body.Bytes()

	// Skip ETag logic if handler already set one or returned 304.
	if c.headers.Get("ETag") != "" || c.code == http.StatusNotModified {
		c.writeBody(body)
		return
	}

	etag := computeETag(body)

	// Check If-None-Match precondition.
	ifNoneMatch := c.req.Header.Get("If-None-Match")
	if etagMatches(etag, ifNoneMatch) {
		c.ResponseWriter.Header().Set("ETag", etag)
		c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
		if cc := c.headers.Get("Cache-Control"); cc != "" {
			c.ResponseWriter.Header().Set("Cache-Control", cc)
		}
		c.ResponseWriter.WriteHeader(http.StatusNotModified)
		return
	}

	// Attach ETag and write full response.
	c.headers.Set("ETag", etag)
	c.writeBody(body)
}

// writeBody writes the response headers and body, compressing if applicable.
func (c *compressWriter) writeBody(body []byte) {
	c.headers.Del("Content-Length")

	if c.encoding != "" && int64(len(body)) >= minCompressSize {
		c.headers.Set("Content-Encoding", c.encoding)
	}

	for k, vv := range c.headers {
		for _, v := range vv {
			c.ResponseWriter.Header().Add(k, v)
		}
	}
	c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
	c.ResponseWriter.WriteHeader(c.code)

	if c.encoding != "" && int64(len(body)) >= minCompressSize {
		switch c.encoding {
		case "br":
			br := brotli.NewWriter(c.ResponseWriter)
			br.Write(body)
			br.Close()
		case "gzip":
			gz := gzip.NewWriter(c.ResponseWriter)
			gz.Write(body)
			gz.Close()
		}
	} else {
		c.ResponseWriter.Write(body)
	}
}

func (c *compressWriter) Flush() {
	switch c.encoding {
	case "br":
		if c.br != nil {
			c.br.Flush()
		}
	case "gzip":
		if c.gz != nil {
			c.gz.Flush()
		}
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := c.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// noWriterToFile wraps an http.File to prevent io.WriterTo (sendfile) optimization,
// forcing data through Write() so the compress middleware can intercept it.
type noWriterToFile struct {
	http.File
}

// noWriterToFS wraps a filesystem to strip io.WriterTo from returned files.
type noWriterToFS struct {
	http.FileSystem
}

func (fs noWriterToFS) Open(name string) (http.File, error) {
	f, err := fs.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	return noWriterToFile{File: f}, nil
}
