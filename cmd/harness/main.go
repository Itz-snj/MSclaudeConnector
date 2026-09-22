package main

import (
	"bufio"
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
	"sync"
	"syscall"
	"time"

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
	"github.com/mdp/qrterminal/v3"
)

const defaultPairTTL = 30 * time.Minute

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
	pairTTL := fs.Duration("pair-ttl", defaultPairTTL, "Pairing token lifetime (0 = never expires)")
	insecureHTTP := fs.Bool("insecure-http", false, "Serve plaintext HTTP/WS (trusted/private bind only)")
	_ = fs.Parse(args)

	cfg.Port = *port
	cfg.Bind = *bind
	cfg.SetDataDir(*dataDir)

	if err := cfg.EnsureDirs(); err != nil {
		log.Error("failed to create data directories", "error", err)
		os.Exit(1)
	}
	if err := cfg.Save(); err != nil {
		log.Error("failed to save config", "error", err)
		os.Exit(1)
	}

	keyExisted := fileExists(cfg.KeyFile)
	cert, pins, regenerated, err := certutil.LoadOrGenerate(cfg.CertFile, cfg.KeyFile, buildSANs())
	if err != nil {
		log.Error("failed to load/generate TLS certificate", "error", err)
		os.Exit(1)
	}
	if regenerated {
		if !keyExisted {
			log.Error("host identity changed; all paired devices must re-pair", "spki", pins.SPKI)
		} else {
			log.Warn("TLS certificate regenerated for new SANs; SPKI pin unchanged", "spki", pins.SPKI)
		}
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "harness.db"))
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}

	authz := auth.New(st)
	pairingToken, err := authz.GeneratePairingTokenTTL(*pairTTL)
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

	hubHandler := hub.New(hub.Config{SessionID: session.ID, Approver: installApprover(log)}, log, st, authz, adapter)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go hubHandler.Run(ctx)

	addrs, err := advertisedAddrs(cfg.Bind)
	if err != nil {
		addrs = []string{"127.0.0.1"}
	}

	scheme := "https"
	wsScheme := "wss"
	if *insecureHTTP {
		if err := validateInsecureBind(addrs); err != nil {
			log.Error("refusing to start", "error", err)
			os.Exit(1)
		}
		scheme, wsScheme = "http", "ws"
		log.Warn("SERVING PLAINTEXT HTTP/WS — any device on this network can observe traffic", "bind", addrs)
	}

	pairingInfo := pairingInfo{
		Addrs:  addrs,
		Port:   cfg.Port,
		Token:  pairingToken,
		SPKI:   pins.SPKI,
		Code:   auth.ApprovalCode(pairingToken),
		Scheme: scheme,
		WS:     wsScheme,
	}
	pairingInfoJSON, _ := json.Marshal(pairingInfo)

	log.Info("harness daemon starting",
		"addr", resolveAddr(cfg.Bind, cfg.Port),
		"session", session.ID,
		"spki", pins.SPKI,
		"scheme", scheme)

	webHandler, err := webui.Handler()
	if err != nil {
		log.Error("failed to load web UI", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", hubHandler)
	mux.Handle("/", webHandler)

	server := &http.Server{
		Addr:      resolveAddr(cfg.Bind, cfg.Port),
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}

	broadcaster, err := discovery.NewBroadcaster("harness-"+session.ID[:8], cfg.Port, pins.SPKI)
	if err != nil {
		log.Warn("mDNS broadcast failed", "error", err)
	}
	if broadcaster != nil {
		defer broadcaster.Shutdown()
	}

	go func() {
		fmt.Println()
		fmt.Println("Pair this device:")
		fmt.Println("  Token:", pairingToken)
		fmt.Println("  Code: ", auth.ApprovalCode(pairingToken))
		fmt.Println("  SPKI: ", pins.SPKI)
		fmt.Println("  JSON:", string(pairingInfoJSON))
		fmt.Println()
		qrterminal.Generate(string(pairingInfoJSON), qrterminal.L, os.Stdout)
		fmt.Println()
		if *insecureHTTP {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("server error", "error", err)
			}
		} else {
			if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("server error", "error", err)
			}
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
	qr := fs.Bool("qr", false, "Generate a token and print a pairing QR")
	pairTTL := fs.Duration("pair-ttl", defaultPairTTL, "Pairing token lifetime (0 = never expires)")
	dataDir := fs.String("data", cfg.DataDir, "Directory for SQLite DB, certs, and session data")
	_ = fs.Parse(args)
	cfg.SetDataDir(*dataDir)

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
	case *qr:
		_, pins, _, err := certutil.LoadOrGenerate(cfg.CertFile, cfg.KeyFile, buildSANs())
		if err != nil {
			log.Error("failed to load/generate TLS certificate", "error", err)
			os.Exit(1)
		}
		token, err := authz.GeneratePairingTokenTTL(*pairTTL)
		if err != nil {
			log.Error("failed to generate token", "error", err)
			os.Exit(1)
		}
		info := pairingInfo{
			Addrs:  mustAdvertisedAddrs(cfg.Bind),
			Port:   cfg.Port,
			Token:  token,
			SPKI:   pins.SPKI,
			Code:   auth.ApprovalCode(token),
			Scheme: "https",
			WS:     "wss",
		}
		raw, _ := json.Marshal(info)
		fmt.Println("Token:", token)
		fmt.Println("Code: ", info.Code)
		fmt.Println("JSON:", string(raw))
		fmt.Println()
		qrterminal.Generate(string(raw), qrterminal.L, os.Stdout)
	case *generate:
		token, err := authz.GeneratePairingTokenTTL(*pairTTL)
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
	dataDir := fs.String("data", cfg.DataDir, "Directory for SQLite DB, certs, and session data")
	_ = fs.Parse(args)
	cfg.SetDataDir(*dataDir)

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

// pairingInfo is the JSON payload encoded in the pairing QR.
type pairingInfo struct {
	Addrs  []string `json:"addrs"`
	Port   int      `json:"port"`
	Token  string   `json:"token"`
	SPKI   string   `json:"spki"`
	Code   string   `json:"code"`
	Scheme string   `json:"scheme"`
	WS     string   `json:"ws"`
}

// terminalApprover prompts the host operator to confirm an inbound pairing.
type terminalApprover struct {
	mu      sync.Mutex
	lines   chan string
	started bool
}

// installApprover returns a terminal approver, or nil when stdin is not a TTY.
// Without a TTY the daemon only accepts tokens approved out-of-band via
// `harness pair --approve`.
func installApprover(log logger.Logger) hub.Approver {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		log.Warn("stdin is not a terminal; device pairing requires `harness pair --approve`")
		return nil
	}
	return &terminalApprover{}
}

func (a *terminalApprover) RequestApproval(ctx context.Context, req hub.ApprovalRequest) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	lines := a.ensureReader()
	code := auth.ApprovalCode(req.Token)
	defaultName := nameFromUserAgent(req.UserAgent)

	fmt.Println()
	fmt.Println("=== Pairing request ===")
	fmt.Printf("  From:  %s\n", req.RemoteAddr)
	fmt.Printf("  Agent: %s\n", req.UserAgent)
	fmt.Printf("  Code:  %s\n", code)
	fmt.Printf("Approve? [y/N] (60s) name [%s]: ", defaultName)

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	select {
	case <-timeoutCtx.Done():
		fmt.Println("\n(approval timed out)")
		return "", false
	case line, ok := <-lines:
		if !ok {
			return "", false
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			return "", false
		}
		answer := strings.ToLower(fields[0])
		if answer != "y" && answer != "yes" {
			return "", false
		}
		name := defaultName
		if len(fields) > 1 {
			name = strings.Join(fields[1:], " ")
		}
		return name, true
	}
}

func (a *terminalApprover) ensureReader() chan string {
	if !a.started {
		a.started = true
		a.lines = make(chan string, 4)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				a.lines <- sc.Text()
			}
			close(a.lines)
		}()
	}
	return a.lines
}

func buildSANs() certutil.SANs {
	dns := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" {
		dns = append(dns, host, host+".local")
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if addrs, err := netutil.LANIPs(); err == nil {
		for _, a := range addrs {
			if ip := net.ParseIP(a); ip != nil {
				ips = append(ips, ip)
			}
		}
	}
	return certutil.SANs{DNSNames: dns, IPs: ips}
}

func advertisedAddrs(bind string) ([]string, error) {
	addrs, err := netutil.LANIPs()
	if err != nil {
		return nil, err
	}
	if bind != "" && bind != "lan" && bind != "tailnet" {
		addrs = append([]string{bind}, addrs...)
	}
	return addrs, nil
}

func mustAdvertisedAddrs(bind string) []string {
	addrs, err := advertisedAddrs(bind)
	if err != nil {
		return []string{"127.0.0.1"}
	}
	return addrs
}

// validateInsecureBind refuses plaintext HTTP unless every advertised address
// is loopback, RFC1918, or CGNAT (100.64.0.0/10).
func validateInsecureBind(addrs []string) error {
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			return fmt.Errorf("cannot validate bind address %q", a)
		}
		if !isPrivateIP(ip) {
			return fmt.Errorf("bind address %s is not loopback, RFC1918, or CGNAT", a)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
	}
	return false
}

func nameFromUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "device"
	}
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "android"):
		return "Android"
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ipad"):
		return "iOS"
	case strings.Contains(lower, "chrome"):
		return "Chrome"
	case strings.Contains(lower, "firefox"):
		return "Firefox"
	case strings.Contains(lower, "safari"):
		return "Safari"
	}
	if len(ua) > 32 {
		return ua[:32]
	}
	return ua
}

func resolveAddr(bind string, port int) string {
	host := ""
	if bind != "" && bind != "lan" && bind != "tailnet" {
		host = bind
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
