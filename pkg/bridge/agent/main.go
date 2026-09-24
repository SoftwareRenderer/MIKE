// Command sandbox-bridge is the in-container half of the sandbox bridge. It runs as
// /sandbox-bridge and services tool calls over the stdin/stdout of one "podman exec -i",
// using the framing in pkg/bridge/protocol. No listener, no port: the process lives exactly
// as long as the host-side exec. Logs go to stderr only - stdout is the protocol.
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"mike/pkg/logging"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	// Bridge agent: communicates over stdin/stdout with the host-side relay.
	// Runs inside gVisor containers via `podman exec -i ... /sandbox-bridge`.
	// The two pipes are the whole transport: no listener, no port, no deadline a socket
	// could carry, so nothing here is typed as a connection.
	h := newHandler()
	h.closed.Store(false)

	// Handle SIGTERM gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logging.For(logging.Bridge).Info("agent shutting down")
		h.close()
		os.Exit(0)
	}()

	h.handleConn(os.Stdin, os.Stdout)
}
