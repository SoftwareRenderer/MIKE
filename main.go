package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/trace"
	"syscall"
	"time"

	"mike/backend/server"
	"mike/backend/tls"

	"mike/pkg/logging"
	"net/http"
	_ "net/http/pprof"

	"mike/backend/startup"
)

func main() {
	port := flag.Int("port", 0, "Server listen port (default: $MIKE_PORT or 3000)")
	listen := flag.String("listen", "", "Server listen address (default: $MIKE_LISTEN or 127.0.0.1; use 0.0.0.0 or :: to listen on all interfaces)")
	dataDir := flag.String("data-dir", "", "Data directory for DB, TLS certs, trace files (default: $MIKE_DATA_DIR or ./data)")
	worktreesDir := flag.String("worktrees-dir", "", "Runtime override for the git worktrees dir (default: $MIKE_WORKTREES_DIR or ./.worktrees)")
	frontendDist := flag.String("frontend-dist", "", "Development only: serve the SPA built in this directory instead of the one compiled in (default: $MIKE_FRONTEND_DIST or ./frontend/dist). A release build refuses it.")
	noTLS := flag.Bool("no-tls", false, "Disable TLS (plain HTTP); overrides $MIKE_TLS")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate file (default: auto-generated self-signed)")
	tlsKey := flag.String("tls-key", "", "Path to TLS private key file")
	allowedHosts := flag.String("allowed-hosts", "", "Hostnames accepted in the Host header, comma-separated (default: $MIKE_ALLOWED_HOSTS; required behind a reverse proxy that forwards a public domain)")
	logLevel := flag.String("log-level", "", "Log verbosity: debug, info, warn or error (default: $MIKE_LOG_LEVEL or info)")
	flag.Parse()

	cfg, err := startup.Resolve(startup.Flags{
		Port:         *port,
		Listen:       *listen,
		DataDir:      *dataDir,
		WorktreesDir: *worktreesDir,
		FrontendDist: *frontendDist,
		NoTLS:        *noTLS,
		TLSCertPath:  *tlsCert,
		TLSKeyPath:   *tlsKey,
		AllowedHosts: *allowedHosts,
		LogLevel:     *logLevel,
	})
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	logging.SetLevel(cfg.LogLevel)

	for _, warning := range cfg.Warnings {
		logging.For(logging.Main).Warn(warning)
	}

	assets, assetSource, err := frontendAssets(cfg.FrontendDist)
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	logging.For(logging.Main).Infof("serving the frontend from: %s", assetSource)

	// Potentially contains API keys and passwords, so this is never on by default.
	if cfg.PProf {
		go func() {
			logging.For(logging.Main).Info("pprof server starting on localhost:6060")
			logging.For(logging.Main).Errorf("pprof server stopped: %v", http.ListenAndServe("localhost:6060", nil))
		}()
	}

	// Owner-only: the database and the TLS key material live here.
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	for _, name := range cfg.WeakSecrets() {
		logging.For(logging.Main).Warnf("⚠ WARNING: %s is less than %d characters. A stronger secret is recommended for production use.", name, startup.MinSecretLength)
	}

	// Checked before any startup side effect: a refused boot leaves nothing behind.
	if cfg.RefusesWeakSecret() {
		log.Fatalf("Refusing to start: MIKE_LOGIN_PASSWORD is weak (fewer than 8 characters, or a well-known default) while the server listens on %q, which is reachable from other machines. "+
			"Listen on loopback instead (--listen 127.0.0.1 or MIKE_LISTEN=127.0.0.1), or set a strong MIKE_LOGIN_PASSWORD of at least 8 characters.",
			cfg.Listen)
	}

	tracePath := filepath.Join(cfg.DataDir, "kanban.trace")
	traceFile, err := openTraceFile(tracePath)
	if err != nil {
		log.Fatalf("Failed to create trace file: %v", err)
	}
	if err := trace.Start(traceFile); err != nil {
		traceFile.Close()
		log.Fatalf("Failed to start flight recorder: %v", err)
	}
	logging.For(logging.Main).Infof("Flight recorder started -> %s", tracePath)

	rotationDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				trace.Stop()
				traceFile.Close()
				oldName := tracePath + ".old"
				_ = os.Rename(tracePath, oldName)
				f, err := openTraceFile(tracePath)
				if err != nil {
					logging.For(logging.Trace).Warnf("rotate: failed to create new trace: %v", err)
					continue
				}
				traceFile = f
				if err := trace.Start(f); err != nil {
					f.Close()
					logging.For(logging.Trace).Warnf("rotate: failed to restart trace: %v", err)
					continue
				}
			case <-rotationDone:
				return
			}
		}
	}()

	var tlsCfg *tls.Config
	if cfg.TLSDisabled {
		tlsCfg = tls.LoadDisabled()
	} else {
		tlsCfg, err = tls.LoadWithCert(cfg.DataDir, cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			log.Fatalf("Failed to load TLS config: %v", err)
		}
	}

	srv, err := server.NewServer(cfg, tlsCfg, assets)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	repairCtx, cancelRepair := srv.Detached()
	go srv.RepairExtensionFiles(repairCtx)

	// A Serve() failure must not end the process where it happens: take the same cleanup
	// a signal does (trace close, Shutdown), then exit 1 so a supervisor restarts. Never
	// log.Fatalf here — it skips that cleanup.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	exitCode := 0
	restartRequested := false
	select {
	case <-sigCh:
		logging.For(logging.Main).Info("Shutting down...")
	case <-srv.RestartRequests():
		// A staged release is on disk and only a new exec sees it. This is the same path a SIGTERM
		// takes: a restart that cleaned up less than a stop would be the worse of the two.
		logging.For(logging.Main).Info("Restart requested to apply a staged release...")
		restartRequested = true
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.For(logging.Main).Warnf("Server error: %v", err)
			exitCode = 1
		} else {
			logging.For(logging.Main).Info("Shutting down...")
		}
	}

	cancelRepair() // the repair pass is bounded by this context; nothing joins it
	close(rotationDone)
	trace.Stop()
	traceFile.Close()
	// Bounded, so a blocked cleanup step cannot leave the process holding the data directory.
	if err := server.RunShutdownWithDeadline(srv.Shutdown); err != nil {
		logging.For(logging.Main).Infof("Shutdown: %v", err)
		if errors.Is(err, server.ErrShutdownDeadline) && exitCode == 0 {
			// A wedged subsystem is worth a non-zero exit: the process left work behind.
			exitCode = 1
		}
	}
	// The exec is last, after every teardown: the new process opens the same database, so the old one
	// must have closed it. This is a re-exec, not a subprocess inheriting the server's environment.
	if restartRequested {
		exe, err := os.Executable()
		if err != nil {
			logging.For(logging.Main).Errorf("restart: cannot find this binary: %v", err)
			os.Exit(1)
		}
		logging.For(logging.Main).Infof("re-executing %s", exe)
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			// The staged release is on disk, so the next start applies it either way; non-zero says
			// this one did not.
			logging.For(logging.Main).Errorf("restart: re-executing %s failed: %v; the staged release is on disk and the next start will apply it", exe, err)
			os.Exit(1)
		}
	}
	os.Exit(exitCode)
}

// The trace holds ~30s of runtime internals, so the file is 0600.
func openTraceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
