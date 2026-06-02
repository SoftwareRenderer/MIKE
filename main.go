package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"kanban/backend"
)

func main() {
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

	server, err := backend.NewServer(*dbPath, worktreesDir)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.Serve(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")
}