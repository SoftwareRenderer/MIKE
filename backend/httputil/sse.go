package httputil

// Server-Sent Events is one grammar and every stream in this server writes it by hand today, with
// slightly different header sets and framing each time. This is that one grammar: headers here, one
// frame per call, flushed as written, so a new stream cannot forget a header or drift in framing.
//
// What a payload holds stays the caller's business. A stream that carries JSON calls JSON or EventJSON;
// one that carries plain progress lines calls Data with the line.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSE is a claimed event-stream response.
type SSE struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSE claims the response as an event stream and reports whether this ResponseWriter can flush. When
// it cannot, nothing is written and ok is false: the caller answers the failure its own way — a 500 for a
// client that asked for a stream it cannot hold, a drained channel for an operation that must still
// finish. X-Accel-Buffering is set because a proxy that buffers the body turns a progress stream into one
// answer that arrives at the end.
func NewSSE(w http.ResponseWriter) (s *SSE, ok bool) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	return &SSE{w: w, flusher: flusher}, true
}

// Data writes one frame whose payload goes out verbatim. A write error is the client leaving; the stream
// that cares stops on it, the one that does not ignores it.
func (s *SSE) Data(payload string) error {
	_, err := fmt.Fprintf(s.w, "data: %s\n\n", payload)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// JSON writes one frame carrying v. A value that will not marshal writes nothing: a progress event that
// cannot be encoded is not worth ending the stream over.
func (s *SSE) JSON(v any) {
	if data, err := json.Marshal(v); err == nil {
		_ = s.Data(string(data))
	}
}

// Event writes one named frame. The error is the client leaving mid-frame.
func (s *SSE) Event(name, payload string) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// EventJSON writes one named frame carrying v, dropping a value that will not marshal.
func (s *SSE) EventJSON(name string, v any) {
	if data, err := json.Marshal(v); err == nil {
		_ = s.Event(name, string(data))
	}
}
