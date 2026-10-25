package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
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

// encodingPriority holds the encodings this server can write, in preference order. A client
// offering only something else is answered with identity bytes, because a client that cannot
// name a decoder usually has none.
var encodingPriority = map[string]int{
	"br":   1,
	"gzip": 2,
}

type compressEncoder interface {
	io.WriteCloser
	Flush() error
}

func encodingWriter(encoding string, w io.Writer) (compressEncoder, bool) {
	switch encoding {
	case "br":
		return brotli.NewWriter(w), true
	case "gzip":
		return gzip.NewWriter(w), true
	}
	return nil, false
}

func acceptToken(entry string) (string, bool) {
	name, params, _ := strings.Cut(entry, ";")
	for param := range strings.SplitSeq(params, ";") {
		key, value, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && q == 0 {
			return "", false
		}
	}
	return strings.TrimSpace(name), true
}

func selectEncoding(acceptEncoding string) string {
	best := ""
	bestRank := 0

	for entry := range strings.SplitSeq(acceptEncoding, ",") {
		encoding, ok := acceptToken(entry)
		if !ok {
			continue
		}
		encoding = strings.ToLower(encoding)
		rank, ok := encodingPriority[encoding]
		if !ok {
			continue
		}
		if best == "" || rank < bestRank {
			best, bestRank = encoding, rank
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
	passThroughWriter
	headers       http.Header
	body          bytes.Buffer
	code          int
	wroteHeader   bool
	committed     bool
	encoding      string
	method        string
	req           *http.Request
	enc           compressEncoder
	contentLength int64 // parsed from Content-Length header, -1 if unknown
}

func newCompressWriter(w http.ResponseWriter, r *http.Request, encoding string) *compressWriter {
	return &compressWriter{
		passThroughWriter: passThroughWriter{w},
		headers:           make(http.Header),
		encoding:          encoding,
		method:            r.Method,
		req:               r,
		contentLength:     -1,
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
		if c.enc != nil {
			return c.enc.Write(p)
		}
		return c.ResponseWriter.Write(p)
	}

	// Bodies that must leave as the handler wrote them. An SSE stream needs real-time
	// flushes; an image is bytes the browser already compressed, so the GET buffer below
	// would hold several times the file to hash an ETag, then brotli it for nothing. An
	// `attachment` disposition is the same case: it is how a handler says the response is a
	// file to save, not a document to render.
	if ct := c.headers.Get("Content-Type"); ct == "text/event-stream" || strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(c.headers.Get("Content-Disposition"), "attachment") {
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
	c.flushHeaders()
	if c.body.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.body.Bytes())
		c.body.Reset()
	}
}

// flushHeaders writes the buffered headers, the status, and the Vary a compressed response carries.
func (c *compressWriter) flushHeaders() {
	for k, vv := range c.headers {
		for _, v := range vv {
			c.ResponseWriter.Header().Add(k, v)
		}
	}
	c.ResponseWriter.Header().Set("Vary", "Accept-Encoding")
	c.ResponseWriter.WriteHeader(c.code)
}

func (c *compressWriter) commit() {
	c.committed = true
	c.headers.Del("Content-Length")

	enc, ok := encodingWriter(c.encoding, c.ResponseWriter)
	if ok {
		c.enc = enc
		c.headers.Set("Content-Encoding", c.encoding)
	}

	c.flushHeaders()

	if ok {
		c.enc.Write(c.body.Bytes())
		return
	}
	c.ResponseWriter.Write(c.body.Bytes())
}

func (c *compressWriter) finish() {
	// GET requests that haven't been committed (e.g. SSE streamed out) get ETag handling.
	if c.method == http.MethodGet && !c.committed {
		c.finishGET()
		return
	}

	if !c.committed {
		c.flushHeaders()
		c.ResponseWriter.Write(c.body.Bytes())
		return
	}

	if c.enc != nil {
		c.enc.Close()
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

	// The size check comes first, so the middleware allocates no writer for a body
	// it will send uncompressed.
	var enc compressEncoder
	compress := false
	if len(body) >= minCompressSize {
		enc, compress = encodingWriter(c.encoding, c.ResponseWriter)
	}
	if compress {
		c.headers.Set("Content-Encoding", c.encoding)
	}

	c.flushHeaders()

	if !compress {
		c.ResponseWriter.Write(body)
		return
	}

	enc.Write(body)
	enc.Close()
}

func (c *compressWriter) Flush() {
	if c.enc != nil {
		c.enc.Flush()
	}
	c.passThroughWriter.Flush()
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
