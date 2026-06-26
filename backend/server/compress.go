package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// minCompressSize is the minimum response size to bother with compression.
const minCompressSize = 1024

// compressionLevel maps encoding name to its preference rank (lower = preferred).
var encodingPriority = map[string]int{
	"br":   1,
	"gzip": 2,
	"deflate": 3,
	"*":    4,
}

// selectEncoding picks the best compression encoding the client supports.
// Returns "" if no supported encoding is found.
func selectEncoding(acceptEncoding string) string {
	if acceptEncoding == "" {
		return ""
	}

	best := ""
	bestRank := 5 // worse than wildcard

	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		// Strip quality factor (e.g. "br;q=0.8" -> "br")
		encoding := strings.TrimSpace(strings.Split(part, ";")[0])

		rank, ok := encodingPriority[strings.ToLower(encoding)]
		if !ok {
			continue
		}
		if rank < bestRank {
			bestRank = rank
			best = encoding
		}
	}

	return best
}

// CompressMiddleware wraps an http.Handler to compress responses when the
// client supports it (brotli > gzip). Only compresses bodies >= minCompressSize.
func CompressMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip compression for WebSocket upgrades (they hijack the connection).
		if strings.Contains(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}

		encoding := selectEncoding(r.Header.Get("Accept-Encoding"))
		if encoding == "" {
			w.Header().Set("Vary", "Accept-Encoding")
			next.ServeHTTP(w, r)
			return
		}

		cw := newCompressWriter(w, encoding)
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
	gz            *gzip.Writer
	br            *brotli.Writer
	contentLength int64 // parsed from Content-Length header, -1 if unknown
}

func newCompressWriter(w http.ResponseWriter, encoding string) *compressWriter {
	return &compressWriter{
		ResponseWriter: w,
		headers:        make(http.Header),
		encoding:       encoding,
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

	// Buffer data until we can decide.
	n, err := c.body.Write(p)
	if err != nil {
		return n, err
	}

	// Decide once we have enough data or know the declared size.
	estimated := int64(c.body.Len())
	if c.contentLength >= 0 && c.contentLength > estimated {
		estimated = c.contentLength
	}
	if estimated >= minCompressSize {
		c.commit()
	}
	return n, err
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
