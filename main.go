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
	"strings"
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
	bind := flag.String("bind", "", "Server bind address (default: $MIKE_BIND or 127.0.0.1; use 0.0.0.0 or :: to listen on all interfaces)")
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

	// Ensure data directory exists. It holds the database (session hashes,
	// encrypted secrets) and the TLS key material, so it is owner-only.
	if err := os.MkdirAll(effectiveDataDir, 0o700); err != nil {
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

	// Bind address: flag > env var > loopback. Loopback by default because the API
	// is RCE-complete (terminal WebSocket, git operations, file writes) and must
	// never land on a public interface by accident; an explicit 0.0.0.0 / :: is the
	// deliberate escape hatch for containers and reverse proxies.
	effectiveBind := *bind
	if effectiveBind == "" {
		effectiveBind = os.Getenv("MIKE_BIND")
	}
	if effectiveBind == "" {
		effectiveBind = defaultBind
	}
	addr := listenAddr(effectiveBind, effectivePort)
	if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
		log.Fatalf("Invalid bind address %q (from --bind / MIKE_BIND): %v", addr, err)
	}

	// A weak master password is fine on loopback (dev flow) but not on an address
	// other hosts can reach. Checked before any startup side effect (trace file,
	// TLS material) so a refusal leaves nothing behind.
	if refusesWeakSecret(effectiveBind, os.Getenv("MIKE_LOGIN_PASSWORD")) {
		log.Fatalf("Refusing to start: MIKE_LOGIN_PASSWORD is weak (fewer than 8 characters, or a well-known default) while the server listens on %q, which is reachable from other machines. "+
			"Bind to loopback instead (--bind 127.0.0.1 or MIKE_BIND=127.0.0.1), or set a strong MIKE_LOGIN_PASSWORD of at least 8 characters.",
			effectiveBind)
	}

	// Start flight recorder for crash diagnostics (always-on, ~1% overhead).
	// The trace holds ~30s of runtime memory (request paths, strings that may
	// appear in arguments), so it is written owner-only.
	tracePath := filepath.Join(effectiveDataDir, "kanban.trace")
	traceFile, err := openTraceFile(tracePath)
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
				f, err := openTraceFile(tracePath)
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

	// Bind address was resolved (and validated against the master password)
	// above, before the flight recorder started.

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

// openTraceFile creates (or truncates) a flight-recorder trace file. The trace
// captures ~30s of runtime internals, so it is owner-only (0600), like the data
// directory holding the DB and TLS keys.
func openTraceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// defaultBind is the bind address used when neither --bind nor MIKE_BIND is
// set: loopback only, so the (RCE-complete) API is never exposed by default.
const defaultBind = "127.0.0.1"

// listenAddr builds the server listen address from a bind address and port.
// An empty bind means loopback; "0.0.0.0"/"::" are the explicit
// all-interfaces escape hatch (a bare ":port" covers both families).
func listenAddr(bind string, port int) string {
	switch bind {
	case "":
		return net.JoinHostPort(defaultBind, strconv.Itoa(port))
	case "0.0.0.0", "::":
		return fmt.Sprintf(":%d", port)
	default:
		return net.JoinHostPort(bind, strconv.Itoa(port))
	}
}

// weakPasswords are master passwords that must never guard a non-loopback bind.
var weakPasswords = map[string]struct{}{
	"test": {}, "dev": {}, "admin": {}, "password": {}, "pass": {}, "changeme": {},
}

// minPasswordLength is the minimum master password length accepted on an
// exposed (non-loopback) bind.
const minPasswordLength = 8

// isLoopbackBind reports whether a bind address accepts connections from the
// loopback interface only. "" is the default (loopback); an explicit
// "0.0.0.0"/"::" or any routable address is not.
func isLoopbackBind(bind string) bool {
	b := strings.TrimSpace(bind)
	if b == "" || strings.EqualFold(b, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(b, "[]"))
	return ip != nil && ip.IsLoopback()
}

// refusesWeakSecret reports whether startup must abort: a weak MIKE_LOGIN_PASSWORD
// (shorter than minPasswordLength, or a well-known default) is fatal unless the
// server binds loopback only. Loopback + weak stays allowed so the dev flow works.
func refusesWeakSecret(bind, password string) bool {
	if isLoopbackBind(bind) {
		return false
	}
	pw := strings.TrimSpace(password)
	if len(pw) < minPasswordLength {
		return true
	}
	_, known := weakPasswords[strings.ToLower(pw)]
	return known
}
