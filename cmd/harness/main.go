package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent/claude"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent/mock"
	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/certutil"
	"github.com/Itz-snj/MSclaudeConnector/internal/config"
	"github.com/Itz-snj/MSclaudeConnector/internal/discovery"
	"github.com/Itz-snj/MSclaudeConnector/internal/hub"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/netutil"
	"github.com/Itz-snj/MSclaudeConnector/internal/store"
	"github.com/Itz-snj/MSclaudeConnector/internal/webui"
)

func main() {
	log := logger.New()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serveCmd(cfg, log, os.Args[2:])
	case "pair":
		pairCmd(cfg, log, os.Args[2:])
	case "status":
		statusCmd(cfg, log, os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: harness <command> [options]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  serve   Run the host daemon")
	fmt.Fprintln(os.Stderr, "  pair    Manage device pairing")
	fmt.Fprintln(os.Stderr, "  status  Show daemon/session status")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Use 'harness <command> -h' for command-specific options.")
}

func serveCmd(cfg *config.Config, log logger.Logger, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", cfg.Port, "WebSocket/HTTPS server port")
	bind := fs.String("bind", cfg.Bind, "Bind target: 'lan', 'tailnet', or a specific IP")
	dataDir := fs.String("data", cfg.DataDir, "Directory for SQLite DB, certs, and session data")
	agentType := fs.String("agent", "claude", "Agent type: 'claude' or 'mock'")
	workDir := fs.String("dir", ".", "Working directory for the agent session")
	_ = fs.Parse(args)

	cfg.Port = *port
	cfg.Bind = *bind
	cfg.DataDir = *dataDir

	if err := cfg.EnsureDirs(); err != nil {
		log.Error("failed to create data directories", "error", err)
		os.Exit(1)
	}
	if err := cfg.Save(); err != nil {
		log.Error("failed to save config", "error", err)
		os.Exit(1)
	}

	cert, fingerprint, err := certutil.LoadOrGenerate(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		log.Error("failed to load/generate TLS certificate", "error", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "harness.db"))
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}

	authz := auth.New(st)
	pairingToken, err := authz.GeneratePairingToken()
	if err != nil {
		log.Error("failed to generate pairing token", "error", err)
		os.Exit(1)
	}

	var adapter agent.Adapter
	switch *agentType {
	case "claude":
		if _, err := claude.LookPath(); err != nil {
			log.Error("claude executable not found in PATH; install Claude Code or use --agent mock", "error", err)
			os.Exit(1)
		}
		adapter = claude.New(log)
	case "mock":
		adapter = mock.New()
	default:
		log.Error("unknown agent type", "agent", *agentType)
		os.Exit(1)
	}

	absWorkDir, err := filepath.Abs(*workDir)
	if err != nil {
		log.Error("failed to resolve working directory", "error", err)
		os.Exit(1)
	}

	session, err := st.CreateSession(*agentType, absWorkDir)
	if err != nil {
		log.Error("failed to create session", "error", err)
		os.Exit(1)
	}

	if err := adapter.Start(agent.SessionConfig{
		AgentType:  *agentType,
		WorkingDir: absWorkDir,
		Env:        os.Environ(),
	}); err != nil {
		log.Error("failed to start agent adapter", "error", err)
		os.Exit(1)
	}

	hubHandler := hub.New(hub.Config{SessionID: session.ID}, log, st, authz, adapter)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go hubHandler.Run(ctx)

	addrs, err := netutil.LANIPs()
	if err != nil {
		addrs = []string{"127.0.0.1"}
	}
	if cfg.Bind != "" && cfg.Bind != "lan" && cfg.Bind != "tailnet" {
		addrs = append([]string{cfg.Bind}, addrs...)
	}
	pairingInfo := struct {
		Addrs       []string `json:"addrs"`
		Port        int      `json:"port"`
		Token       string   `json:"token"`
		Fingerprint string   `json:"fingerprint"`
	}{
		Addrs:       addrs,
		Port:        cfg.Port,
		Token:       pairingToken,
		Fingerprint: fingerprint,
	}
	pairingInfoJSON, _ := json.Marshal(pairingInfo)

	webHandler, err := webui.Handler()
	if err != nil {
		log.Error("failed to load web UI", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", hubHandler)
	mux.Handle("/", webHandler)

	addr := resolveAddr(cfg.Bind, cfg.Port)
	server := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}

	broadcaster, err := discovery.NewBroadcaster("harness-"+session.ID[:8], cfg.Port, fingerprint)
	if err != nil {
		log.Warn("mDNS broadcast failed", "error", err)
	}
	if broadcaster != nil {
		defer broadcaster.Shutdown()
	}

	go func() {
		log.Info("harness daemon listening", "addr", addr, "session", session.ID, "fingerprint", fingerprint)
		fmt.Println()
		fmt.Println("Pair this device:")
		fmt.Println("  Token:", pairingToken)
		fmt.Println("  Fingerprint:", fingerprint)
		fmt.Println("  JSON:", string(pairingInfoJSON))
		fmt.Println()
		qrterminal.Generate(string(pairingInfoJSON), qrterminal.L, os.Stdout)
		fmt.Println()
		if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	if broadcaster != nil {
		broadcaster.Shutdown()
	}
	_ = adapter.Stop()
	_ = st.Close()
}

func pairCmd(cfg *config.Config, log logger.Logger, args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	generate := fs.Bool("generate", false, "Generate a new pairing token")
	approve := fs.String("approve", "", "Approve pending token (format: token:name)")
	revoke := fs.String("revoke", "", "Revoke a paired device's credential")
	list := fs.Bool("list", false, "List paired devices")
	_ = fs.Parse(args)

	if err := cfg.EnsureDirs(); err != nil {
		log.Error("failed to create data directories", "error", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "harness.db"))
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	authz := auth.New(st)

	switch {
	case *generate:
		token, err := authz.GeneratePairingToken()
		if err != nil {
			log.Error("failed to generate token", "error", err)
			os.Exit(1)
		}
		fmt.Println(token)
	case *approve != "":
		parts := strings.SplitN(*approve, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "usage: --approve token:name")
			os.Exit(1)
		}
		if err := authz.ApprovePairingToken(parts[0], parts[1]); err != nil {
			log.Error("failed to approve token", "error", err)
			os.Exit(1)
		}
		fmt.Println("approved")
	case *revoke != "":
		if err := authz.RevokeDevice(*revoke); err != nil {
			log.Error("failed to revoke device", "error", err)
			os.Exit(1)
		}
		fmt.Println("revoked")
	case *list:
		devices, err := st.ListDevices()
		if err != nil {
			log.Error("failed to list devices", "error", err)
			os.Exit(1)
		}
		for _, d := range devices {
			status := "active"
			if d.Revoked {
				status = "revoked"
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", d.DeviceID, d.Name, d.PairedAt.Format(time.RFC3339), status)
		}
	default:
		fs.Usage()
	}
}

func statusCmd(cfg *config.Config, log logger.Logger, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	_ = fs.Parse(args)

	if err := cfg.EnsureDirs(); err != nil {
		log.Error("failed to create data directories", "error", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "harness.db"))
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	sessions, err := st.ListSessions()
	if err != nil {
		log.Error("failed to list sessions", "error", err)
		os.Exit(1)
	}
	devices, err := st.ListDevices()
	if err != nil {
		log.Error("failed to list devices", "error", err)
		os.Exit(1)
	}

	fmt.Printf("config: port=%d bind=%s dataDir=%s\n", cfg.Port, cfg.Bind, cfg.DataDir)
	fmt.Printf("sessions: %d\n", len(sessions))
	for _, s := range sessions {
		fmt.Printf("  %s\t%s\t%s\t%s\tseq=%d\n", s.ID, s.AgentType, s.WorkingDir, s.Status, s.LastSeq)
	}
	fmt.Printf("devices: %d\n", len(devices))
	for _, d := range devices {
		status := "active"
		if d.Revoked {
			status = "revoked"
		}
		fmt.Printf("  %s\t%s\t%s\n", d.DeviceID, d.Name, status)
	}
}

func resolveAddr(bind string, port int) string {
	host := ""
	if bind != "" && bind != "lan" && bind != "tailnet" {
		host = bind
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}
