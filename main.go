package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"syscall"
	"time"

	"kanban/backend/crypto"
	"kanban/backend/server"
	"kanban/backend/tls"

	"net/http"
	_ "net/http/pprof"
)

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// CLI flags
	port := flag.Int("port", 0, "Server listen port (default: $PORT or 3000)")
	dataDir := flag.String("data-dir", "", "Data directory for DB, TLS certs, trace files (default: $DATA_DIR or ./data)")
	worktreesDir := flag.String("worktrees-dir", "", "Directory for git worktrees (default: $WORKTREES_DIR or ./worktrees)")
	noTLS := flag.Bool("no-tls", false, "Disable TLS (plain HTTP); overrides $KANBAN_TLS")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (default: auto-generated self-signed)")
	tlsKey := flag.String("tls-key", "", "Path to TLS private key file")
	flag.Parse()

	// Resolve effective values: flag > env var > default
	// Port
	effectivePort := *port
	if effectivePort == 0 {
		if v := os.Getenv("PORT"); v != "" {
			fmt.Sscanf(v, "%d", &effectivePort)
		}
		if effectivePort == 0 {
			effectivePort = 3000
		}
	}

	// Data directory
	effectiveDataDir := *dataDir
	if effectiveDataDir == "" {
		effectiveDataDir = os.Getenv("DATA_DIR")
	}
	if effectiveDataDir == "" {
		effectiveDataDir = "./data"
	}

	// Worktrees directory
	effectiveWorktreesDir := *worktreesDir
	if effectiveWorktreesDir == "" {
		effectiveWorktreesDir = os.Getenv("WORKTREES_DIR")
	}
	if effectiveWorktreesDir == "" {
		effectiveWorktreesDir = "./worktrees"
	}

	// Database path (derived from data-dir)
	dbPath := filepath.Join(effectiveDataDir, "kanban.db")

	// Frontend dist
	frontendDist := os.Getenv("FRONTEND_DIST")
	if frontendDist == "" {
		frontendDist = "./frontend/dist"
	}

	// Ensure directories exist
	if err := os.MkdirAll(effectiveWorktreesDir, 0o755); err != nil {
		log.Fatalf("Failed to create worktrees directory: %v", err)
	}
	if err := os.MkdirAll(effectiveDataDir, 0o755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Validate encryption key is set and valid
	if err := crypto.ValidateKey(); err != nil {
		log.Fatalf("Configuration error: %v", err)
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
	// --no-tls flag or KANBAN_TLS=0 env var disables TLS
	var tlsCfg *tls.Config
	if *noTLS || os.Getenv("KANBAN_TLS") == "0" {
		tlsCfg = tls.LoadDisabled()
	} else {
		tlsCfg, err = tls.LoadWithCert(effectiveDataDir, *tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("Failed to load TLS config: %v", err)
		}
	}

	addr := fmt.Sprintf(":%d", effectivePort)

	srv, err := server.NewServer(dbPath, effectiveWorktreesDir, addr, frontendDist, tlsCfg)
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
