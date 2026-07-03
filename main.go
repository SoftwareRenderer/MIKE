package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"syscall"
	"time"

	"kanban/backend/server"

	"net/http"
	_ "net/http/pprof"
)

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()
	// CLI flag for database path (highest priority)
	dbPath := flag.String("db-path", "", "Path to the SQLite database file (default: $DATA_DIR/kanban.db or ./data/kanban.db)")
	flag.Parse()

	// Determine data directory from env var or default
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}

	// Determine database path: CLI flag > constructed from DATA_DIR
	if *dbPath == "" {
		*dbPath = filepath.Join(dataDir, "kanban.db")
	}

	worktreesDir := os.Getenv("WORKTREES_DIR")
	if worktreesDir == "" {
		worktreesDir = "./worktrees"
	}

	// Ensure worktrees directory exists
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		log.Fatalf("Failed to create worktrees directory: %v", err)
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Start flight recorder for crash diagnostics (always-on, ~1% overhead)
	tracePath := filepath.Join(dataDir, "kanban.trace")
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

	srv, err := server.NewServer(*dbPath, worktreesDir)
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
