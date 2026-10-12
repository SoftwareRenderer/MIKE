package httputil

// Server-Sent Events (SSE) provides a standardized framing for event streams.
// This implementation centralizes header setup and framing to ensure consistency
// across all streams.
//
// The payload format is determined by the caller. Use JSON or EventJSON for
// structured data, and Data for plain text.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSE represents an active event-stream response.
type SSE struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSE initializes the response as an event stream. It returns ok=false if the
// ResponseWriter cannot flush. X-Accel-Buffering is disabled to prevent proxies
// from buffering the stream.
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

// Data writes a single frame with the given payload. A write error typically
// indicates the client has disconnected.
func (s *SSE) Data(payload string) error {
	_, err := fmt.Fprintf(s.w, "data: %s\n\n", payload)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// JSON writes a frame containing the JSON-encoded value v. If v cannot
// be marshaled, the frame is omitted.
func (s *SSE) JSON(v any) {
	if data, err := json.Marshal(v); err == nil {
		_ = s.Data(string(data))
	}
}

// Event writes a named frame. A write error typically indicates the
// client has disconnected.
func (s *SSE) Event(name, payload string) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// EventJSON writes a named frame containing the JSON-encoded value v.
// If v cannot be marshaled, the frame is omitted.
func (s *SSE) EventJSON(name string, v any) {
	if data, err := json.Marshal(v); err == nil {
		_ = s.Event(name, string(data))
	}
}
