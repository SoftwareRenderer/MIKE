package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"strconv"
	"syscall"
	"time"

	"kanban/backend/crypto"
	"kanban/backend/server"
	"kanban/backend/tls"

	"net/http"
	_ "net/http/pprof"
)

func main() {
	// Profiling endpoint — disabled by default. Enable with MIKE_PPROF=1.
	// Exposes heap/memory/goroutine profiles that could leak sensitive data
	// (API keys, passwords, etc.) in memory snapshots.
	if os.Getenv("MIKE_PPROF") == "1" {
		go func() {
			log.Println("pprof server starting on localhost:6060")
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	}

	// CLI flags
	port := flag.Int("port", 0, "Server listen port (default: $MIKE_PORT or 3000)")
	bind := flag.String("bind", "", "Server bind address (default: $MIKE_BIND or all interfaces; e.g. 127.0.0.1 for localhost-only)")
	dataDir := flag.String("data-dir", "", "Data directory for DB, TLS certs, trace files (default: $MIKE_DATA_DIR or ./data)")
	worktreesDir := flag.String("worktrees-dir", "", "Runtime override for the git worktrees dir (default: $MIKE_WORKTREES_DIR or ./.worktrees)")
	noTLS := flag.Bool("no-tls", false, "Disable TLS (plain HTTP); overrides $MIKE_TLS")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (default: auto-generated self-signed)")
	tlsKey := flag.String("tls-key", "", "Path to TLS private key file")
	flag.Parse()

	// Resolve effective values: flag > env var > default
	// Port
	effectivePort := *port
	if effectivePort == 0 {
		if v := os.Getenv("MIKE_PORT"); v != "" {
			fmt.Sscanf(v, "%d", &effectivePort)
		}
		if effectivePort == 0 {
			effectivePort = 3000
		}
	}

	// Data directory
	effectiveDataDir := *dataDir
	if effectiveDataDir == "" {
		effectiveDataDir = os.Getenv("MIKE_DATA_DIR")
	}
	if effectiveDataDir == "" {
		effectiveDataDir = "./data"
	}

	worktreesOverride := *worktreesDir
	if worktreesOverride == "" {
		worktreesOverride = os.Getenv("MIKE_WORKTREES_DIR")
	}

	// Database path (derived from data-dir)
	dbPath := filepath.Join(effectiveDataDir, "kanban.db")

	// Frontend dist
	frontendDist := os.Getenv("MIKE_FRONTEND_DIST")
	if frontendDist == "" {
		frontendDist = "./frontend/dist"
	}

	// Ensure data directory exists
	if err := os.MkdirAll(effectiveDataDir, 0o755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Validate encryption key is set and valid
	if err := crypto.ValidateKey(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Warn on weak secrets
	for _, name := range crypto.WarnWeakSecrets() {
		log.Printf("⚠ WARNING: %s is less than 8 characters. A stronger secret is recommended for production use.", name)
	}

	// Start flight recorder for crash diagnostics (always-on, ~1% overhead)
	tracePath := filepath.Join(effectiveDataDir, "kanban.trace")
	traceFile, err := os.Create(tracePath)
	if err != nil {
		log.Fatalf("Failed to create trace file: %v", err)
	}
	if err := trace.Start(traceFile); err != nil {
		traceFile.Close()
		log.Fatalf("Failed to start flight recorder: %v", err)
	}
	log.Printf("Flight recorder started -> %s", tracePath)

	// Rotate trace file every 30s so only recent data is kept at crash time
	rotationDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				trace.Stop()
				traceFile.Close()
				// Rename old trace with timestamp
				oldName := tracePath + ".old"
				_ = os.Rename(tracePath, oldName)
				// Start fresh trace
				f, err := os.Create(tracePath)
				if err != nil {
					log.Printf("[trace] rotate: failed to create new trace: %v", err)
					continue
				}
				traceFile = f
				if err := trace.Start(f); err != nil {
					f.Close()
					log.Printf("[trace] rotate: failed to restart trace: %v", err)
					continue
				}
			case <-rotationDone:
				return
			}
		}
	}()

	// Load TLS configuration
	// --no-tls flag or MIKE_TLS=0 env var disables TLS
	var tlsCfg *tls.Config
	if *noTLS || os.Getenv("MIKE_TLS") == "0" {
		tlsCfg = tls.LoadDisabled()
	} else {
		tlsCfg, err = tls.LoadWithCert(effectiveDataDir, *tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("Failed to load TLS config: %v", err)
		}
	}

	// Bind address: flag > env var > all interfaces (default, backward compatible)
	effectiveBind := *bind
	if effectiveBind == "" {
		effectiveBind = os.Getenv("MIKE_BIND")
	}
	addr := listenAddr(effectiveBind, effectivePort)
	if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
		log.Fatalf("Invalid bind address %q (from --bind / MIKE_BIND): %v", addr, err)
	}

	srv, err := server.NewServer(dbPath, worktreesOverride, addr, frontendDist, tlsCfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Serve(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")
	close(rotationDone)
	trace.Stop()
	traceFile.Close()
	srv.Shutdown(context.Background())
}

// listenAddr builds the server listen address from a bind address and port
func listenAddr(bind string, port int) string {
	switch bind {
	case "", "0.0.0.0", "::":
		return fmt.Sprintf(":%d", port)
	default:
		return net.JoinHostPort(bind, strconv.Itoa(port))
	}
}
