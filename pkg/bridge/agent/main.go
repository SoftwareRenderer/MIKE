// Command sandbox-bridge is the in-container half of the sandbox bridge. It runs as
// /sandbox-bridge and services tool calls over the stdin/stdout of one "podman exec -i",
// using the framing in pkg/bridge/protocol. No listener, no port: the process lives exactly
// as long as the host-side exec. Logs go to stderr only - stdout is the protocol.
package main

import (
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// stdioConn implements net.Conn using stdin/stdout pipes.
// The bridge communicates with the host-side relay over podman exec
// stdin/stdout.
type stdioConn struct {
	r io.Reader
	w io.Writer
}

func (c *stdioConn) Read(b []byte) (int, error)         { return c.r.Read(b) }
func (c *stdioConn) Write(b []byte) (int, error)        { return c.w.Write(b) }
func (c *stdioConn) Close() error                       { return nil }
func (c *stdioConn) LocalAddr() net.Addr                { return &net.UnixAddr{Net: "unix", Name: "stdin"} }
func (c *stdioConn) RemoteAddr() net.Addr               { return &net.UnixAddr{Net: "unix", Name: "stdout"} }
func (c *stdioConn) SetDeadline(t time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return nil }

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	// Bridge agent: communicates over stdin/stdout with the host-side relay.
	// Runs inside gVisor containers via `podman exec -i ... /sandbox-bridge`.
	conn := &stdioConn{
		r: os.Stdin,
		w: os.Stdout,
	}
	h := newHandler()
	h.closed.Store(false)

	// Handle SIGTERM gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		log.Println("agent shutting down")
		h.close()
		os.Exit(0)
	}()

	h.handleConn(conn)
}
