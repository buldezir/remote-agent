// Command rad is the remote-agent server: it runs coding-agent harnesses on
// this machine and serves them to paired phones over WebSocket.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"

	"remote-agent/internal/api"
	"remote-agent/internal/config"
	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/images"
	"remote-agent/internal/netinfo"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
)

const usage = `rad — remote agent server

Usage:
  rad serve [--fake] [--lan]     run the server
  rad pair [--lan] [--print-url] [--web <url>]
                                 show a QR code to pair a phone (or a web client link)
  rad devices [revoke <id>]      list or revoke paired devices
  rad debug <cmd> ...            CLI client for testing (run "rad debug" for help)
  rad install-service [--uninstall]
                                 keep rad running in the background (launchd on macOS, systemd on Linux)
  rad uninstall [--yes]          remove rad's service, config, data, worktrees and checkpoint refs
  rad version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "pair":
		err = pair(os.Args[2:])
	case "devices":
		err = devices(os.Args[2:])
	case "debug":
		err = debug(os.Args[2:])
	case "install-service", "install-launchagent":
		err = installService(os.Args[2:])
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("rad", api.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rad:", err)
		if _, busy := err.(*busyError); busy {
			os.Exit(exitBusy)
		}
		os.Exit(1)
	}
}

func openStore(cfg *config.Config) (*store.Store, error) {
	return store.Open(cfg.DBPath())
}

func buildRegistry(cfg *config.Config) *harness.Registry {
	var hs []harness.Harness
	hs = append(hs, harnessesFor(cfg)...)
	if cfg.Fake {
		hs = append(hs, fake.Harness{})
	}
	return harness.NewRegistry(hs...)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fakeFlag := fs.Bool("fake", false, "expose the scripted fake harness")
	lan := fs.Bool("lan", false, "put LAN addresses in the pairing link")
	verbose := fs.Bool("v", false, "debug logging")
	supervised := fs.Bool("supervised", false, "run under the Remote Agent Server app: status as JSON lines on stdout, and stop when stdin closes")
	fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Fake = cfg.Fake || *fakeFlag
	cfg.LAN = cfg.LAN || *lan
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// Lock and bind before touching the store, so a second rad can't mark the
	// running one's turns as interrupted on its way out.
	lock, err := lockDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	addrs := netinfo.ListenAddrs(cfg)
	lns, err := listen(addrs)
	if err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := os.MkdirAll(cfg.LogsDir(), 0o700); err != nil {
		return err
	}
	hub := events.NewHub()
	st.OnCommit(hub.Publish)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var status *json.Encoder
	if *supervised {
		ctx, status = supervise(ctx)
	}

	reg := buildRegistry(cfg)
	imgs := images.New(cfg.ImagesDir())
	orch := orchestrator.New(st, reg, orchestrator.Options{
		WorktreesDir: cfg.WorktreesDir(), LogsDir: cfg.LogsDir(), Images: imgs, IdleTimeout: cfg.IdleTimeout.Duration, Log: log,
	})
	if err := orch.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	go orch.Run(ctx)

	serverID, err := st.ServerID(ctx)
	if err != nil {
		return err
	}
	srv := api.NewServer(st, hub, orch, fsbrowse.New(cfg.Roots), imgs, serverID, serverName(cfg), log)
	srv.WebOrigins = cfg.WebOrigins
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, len(lns))
	for _, ln := range lns {
		log.Info("listening", "addr", ln.Addr().String())
		go func() { errc <- httpSrv.Serve(ln) }()
	}
	log.Info("rad ready", "urls", netinfo.PairURLs(cfg), "config", cfg.ConfigPath, "data", cfg.DataDir, "roots", cfg.Roots)
	devs, _ := st.Devices(ctx)
	switch {
	case status != nil:
		status.Encode(readyStatus{Event: "ready", Version: api.Version, URLs: netinfo.PairURLs(cfg), Listen: addrs,
			Config: cfg.ConfigPath, Data: cfg.DataDir, PairedDevices: len(devs)})
	case len(devs) == 0:
		fmt.Fprintln(os.Stderr, "\nNo devices paired yet.")
		printPairing(ctx, st, cfg, false, "")
	}
	go func() { // after the QR code, so the log can't split it
		infos := logAgents(ctx, log, reg)
		if status != nil {
			status.Encode(agentsStatus{Event: "agents", Agents: infos})
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
		}
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(sctx)
	orch.Shutdown()
	return nil
}

// listen binds every address, or none: a port that's taken is a busyError.
func listen(addrs []string) ([]net.Listener, error) {
	var lns []net.Listener
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			if errors.Is(err, syscall.EADDRINUSE) {
				return nil, &busyError{a + " is already in use; is another rad running?"}
			}
			return nil, fmt.Errorf("listen %s: %w", a, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	printURL := fs.Bool("print-url", false, "print only the pairing link")
	lan := fs.Bool("lan", false, "put LAN addresses in the pairing link")
	web := fs.String("web", "", "also print a link that pairs the web client at this `URL`")
	fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *lan && len(cfg.PairURLs) > 0 {
		fmt.Fprintln(os.Stderr, "note: --lan has no effect because pair_urls is set in", cfg.ConfigPath)
	}
	cfg.LAN = cfg.LAN || *lan
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if err := printPairing(ctx, st, cfg, *printURL, *web); err != nil || *printURL {
		return err
	}
	printAgents(ctx, os.Stderr, buildRegistry(cfg))
	return nil
}

const pairingTTL = 10 * time.Minute

// serverName is what the app calls this server after pairing.
func serverName(cfg *config.Config) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return netinfo.Hostname()
}

// printPairing prints a new pairing link. With web, the web client's address,
// it also prints a link that opens the web client to pair; with urlOnly, it
// prints only that link, or else the app's link.
func printPairing(ctx context.Context, st *store.Store, cfg *config.Config, urlOnly bool, web string) error {
	code, err := st.CreatePairingCode(ctx, pairingTTL)
	if err != nil {
		return err
	}
	link := netinfo.PairingLink(serverName(cfg), code, netinfo.PairURLs(cfg))
	webLink := ""
	if web != "" {
		// The link rides in the fragment, which browsers don't send to the
		// web client's server.
		webLink = strings.TrimSuffix(web, "/") + "/#pair=" + url.QueryEscape(link)
	}
	if urlOnly {
		fmt.Println(cmp.Or(webLink, link))
		return nil
	}
	fmt.Fprintln(os.Stderr, "\nScan with the Remote Agent app (valid for 10 minutes, single use):")
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stderr, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 2,
	})
	fmt.Fprintf(os.Stderr, "Or paste this link in the app:\n%s\n\n", link)
	if webLink != "" {
		fmt.Fprintf(os.Stderr, "Or open this link to pair the web client:\n%s\n\n", webLink)
	}
	return nil
}

func devices(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if len(args) >= 1 && args[0] == "revoke" {
		if len(args) < 2 || len(args[1]) < 4 {
			return errors.New("usage: rad devices revoke <id-prefix (4+ chars)>")
		}
		n, err := st.DeleteDevice(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("revoked %d device(s); live connections close within 30s\n", n)
		return nil
	}
	ds, err := st.Devices(ctx)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no paired devices; run `rad pair`")
	}
	for _, d := range ds {
		seen := "never"
		if d.LastSeenAt != nil {
			seen = d.LastSeenAt.Local().Format(time.DateTime)
		}
		fmt.Printf("%s  %-24s paired %s  last seen %s\n", d.ID, d.Name, d.CreatedAt.Local().Format(time.DateOnly), seen)
	}
	return nil
}
