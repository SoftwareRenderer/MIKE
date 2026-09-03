package main

import (
	"errors"
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
	listen := flag.String("listen", "", "Server listen address (default: $MIKE_LISTEN or 127.0.0.1; use 0.0.0.0 or :: to listen on all interfaces)")
	dataDir := flag.String("data-dir", "", "Data directory for DB, TLS certs, trace files (default: $MIKE_DATA_DIR or ./data)")
	worktreesDir := flag.String("worktrees-dir", "", "Runtime override for the git worktrees dir (default: $MIKE_WORKTREES_DIR or ./.worktrees)")
	noTLS := flag.Bool("no-tls", false, "Disable TLS (plain HTTP); overrides $MIKE_TLS")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (default: auto-generated self-signed)")
	tlsKey := flag.String("tls-key", "", "Path to TLS private key file")
	allowedHosts := flag.String("allowed-hosts", "", "Hostnames accepted in the Host header, comma-separated (default: $MIKE_ALLOWED_HOSTS; required behind a reverse proxy that forwards a public domain)")
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

	// Extra accepted Host values (DNS-rebinding pinning). A reverse proxy that
	// forwards the public domain needs it listed here, or every request is
	// rejected with HTTP 421 by HostPinMiddleware.
	allowedHostsOverride := *allowedHosts
	if allowedHostsOverride == "" {
		allowedHostsOverride = os.Getenv("MIKE_ALLOWED_HOSTS")
	}

	// Database path (derived from data-dir)
	dbPath := filepath.Join(effectiveDataDir, "kanban.db")

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

	for _, name := range crypto.WarnWeakSecrets() {
		log.Printf("⚠ WARNING: %s is less than 8 characters. A stronger secret is recommended for production use.", name)
	}

	// Listen address: flag > env var > loopback. Loopback by default because the API
	// is RCE-complete (terminal WebSocket, git operations, file writes) and must
	// never land on a public interface by accident; an explicit 0.0.0.0 / :: is the
	// deliberate escape hatch for containers and reverse proxies.
	effectiveListen := *listen
	if effectiveListen == "" {
		effectiveListen = os.Getenv("MIKE_LISTEN")
	}
	if effectiveListen == "" {
		effectiveListen = defaultListenAddr
	}
	addr := listenAddr(effectiveListen, effectivePort)
	if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
		log.Fatalf("Invalid listen address %q (from --listen / MIKE_LISTEN): %v", addr, err)
	}

	// A weak master password is fine on loopback (dev flow) but not on an address
	// other hosts can reach. Checked before any startup side effect (trace file,
	// TLS material) so a refusal leaves nothing behind.
	if refusesWeakSecret(effectiveListen, os.Getenv("MIKE_LOGIN_PASSWORD")) {
		log.Fatalf("Refusing to start: MIKE_LOGIN_PASSWORD is weak (fewer than 8 characters, or a well-known default) while the server listens on %q, which is reachable from other machines. "+
			"Listen on loopback instead (--listen 127.0.0.1 or MIKE_LISTEN=127.0.0.1), or set a strong MIKE_LOGIN_PASSWORD of at least 8 characters.",
			effectiveListen)
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

	// The listen address was resolved (and validated against the master password)
	// above, before the flight recorder started.

	srv, err := server.NewServer(dbPath, worktreesOverride, addr, frontendDist, allowedHostsOverride, tlsCfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// A Serve() failure must not end the process where it happens: take the same cleanup
	// a signal does (trace close, Shutdown), then exit 1 so a supervisor restarts. Never
	// log.Fatalf here — it skips that cleanup.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	exitCode := 0
	select {
	case <-sigCh:
		log.Println("Shutting down...")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Server error: %v", err)
			exitCode = 1
		} else {
			log.Println("Shutting down...")
		}
	}

	close(rotationDone)
	trace.Stop()
	traceFile.Close()
	// Bounded, so a blocked cleanup step cannot leave the process holding the data directory.
	if err := server.RunShutdownWithDeadline(srv.Shutdown); err != nil {
		log.Printf("Shutdown: %v", err)
		if errors.Is(err, server.ErrShutdownDeadline) && exitCode == 0 {
			// A wedged subsystem is worth a non-zero exit: the process left work behind.
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

// openTraceFile creates (or truncates) a flight-recorder trace file. The trace
// captures ~30s of runtime internals, so it is owner-only (0600), like the data
// directory holding the DB and TLS keys.
func openTraceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// defaultListenAddr is the listen address used when neither --listen nor
// MIKE_LISTEN is set: loopback only, so the (RCE-complete) API is never exposed
// by default.
const defaultListenAddr = "127.0.0.1"

// listenAddr builds the server listen address from a listen address and port.
// An empty address means loopback; "0.0.0.0"/"::" are the explicit
// all-interfaces escape hatch (a bare ":port" covers both families).
func listenAddr(listen string, port int) string {
	switch listen {
	case "":
		return net.JoinHostPort(defaultListenAddr, strconv.Itoa(port))
	case "0.0.0.0", "::":
		return fmt.Sprintf(":%d", port)
	default:
		return net.JoinHostPort(listen, strconv.Itoa(port))
	}
}

// weakPasswords are master passwords that must never guard a non-loopback listen address.
var weakPasswords = map[string]struct{}{
	"test": {}, "dev": {}, "admin": {}, "password": {}, "pass": {}, "changeme": {},
}

// minPasswordLength is the minimum master password length accepted on an
// exposed (non-loopback) listen address.
const minPasswordLength = 8

// isLoopbackAddr reports whether a listen address accepts connections from the
// loopback interface only. "" is the default (loopback); an explicit
// "0.0.0.0"/"::" or any routable address is not.
func isLoopbackAddr(listen string) bool {
	b := strings.TrimSpace(listen)
	if b == "" || strings.EqualFold(b, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(b, "[]"))
	return ip != nil && ip.IsLoopback()
}

// refusesWeakSecret reports whether startup must abort: a weak MIKE_LOGIN_PASSWORD
// (shorter than minPasswordLength, or a well-known default) is fatal unless the
// server listens on loopback only. Loopback + weak stays allowed so the dev flow works.
func refusesWeakSecret(listen, password string) bool {
	if isLoopbackAddr(listen) {
		return false
	}
	pw := strings.TrimSpace(password)
	if len(pw) < minPasswordLength {
		return true
	}
	_, known := weakPasswords[strings.ToLower(pw)]
	return known
}
