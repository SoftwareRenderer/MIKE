package server

import (
	"net/http"

	"mike/pkg/logging"
)

// statusLogWriter records the status a handler answers with.
type statusLogWriter struct {
	passThroughWriter
	status      int
	wroteHeader bool
}

// WriteHeader keeps the first status and passes every call through, so net/http still reports a handler
// that sets the status twice.
func (w *statusLogWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

// StatusLogMiddleware reports every answer of 400 or above, and sits just inside the panic recovery so a
// recovered panic is reported too.
func (s *Server) StatusLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusLogWriter{passThroughWriter: passThroughWriter{w}, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if sw.status < http.StatusBadRequest {
			return
		}
		s.logRefusal(r, sw.status, clientIP(r, s.settings.TrustProxy))
	})
}

// routeFor asks the mux which route a request will reach, the same lookup routeScope() uses, because the
// pattern is recorded only on the copy of the request that the mux is handed.
func (s *Server) routeFor(r *http.Request) string {
	if _, pattern := s.mux.Handler(r); pattern != "" {
		return pattern
	}
	return requestRoute(r)
}

// logRefusal writes the one record a refusal leaves, with the status, the route and the address held as
// attributes so a filter survives the wording of the message.
func (s *Server) logRefusal(r *http.Request, status int, addr string) {
	l := logging.For(logging.HTTP).
		With("route", s.routeFor(r)).
		With("method", r.Method).
		With("status", status).
		With("addr", addr)

	switch {
	case status >= http.StatusInternalServerError:
		l.Errorf("server fault")
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusMisdirectedRequest, status == http.StatusTooManyRequests:
		l.Warnf("access refused")
	default:
		l.Debugf("request refused")
	}
}
