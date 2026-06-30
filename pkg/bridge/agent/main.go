// sandbox-bridge runs inside a sandbox container and handles tool calls
// over TCP using the kanban binary protocol.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

const defaultListenAddr = "0.0.0.0:9999"

func main() {
	listenAddr := os.Getenv("KANBAN_AGENT_LISTEN")
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	// Listen on TCP
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	defer listener.Close()

	// Write port to a file so the host can discover it
	portDir := os.Getenv("KANBAN_AGENT_PORT_DIR")
	if portDir != "" {
		if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
			portFile := filepath.Join(portDir, "agent.port")
			_ = os.WriteFile(portFile, []byte(fmt.Sprintf("%d\n", tcpAddr.Port)), 0o644)
		}
	}

	log.Printf("agent listening on %s (CWD=%s)", listener.Addr(), mustGetwd())

	// Signal handler for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Create handler (manages shell sessions)
	h := newHandler()
	defer h.close()

	// Accept loop — single connection from host
	conn, err := listener.Accept()
	if err != nil {
		log.Fatalf("accept: %v", err)
	}
	log.Println("agent: host connected")

	// Wait for signal or connection close
	done := make(chan struct{})
	go func() {
		if err := h.handleConn(conn); err != nil {
			log.Printf("agent: connection ended: %v", err)
		}
		close(done)
	}()

	select {
	case <-sigCh:
		log.Println("agent: shutting down (signal)")
	case <-done:
		log.Println("agent: shutting down (connection closed)")
	}

	conn.Close()
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return wd
}
