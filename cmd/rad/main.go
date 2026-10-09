// Command rad is the remote-agent server: it runs coding-agent harnesses on
// this machine and serves them to paired phones over WebSocket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"

	"remote-agent/internal/api"
	"remote-agent/internal/config"
	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/netinfo"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
)

const usage = `rad — remote agent server

Usage:
  rad serve [--fake] [--lan]     run the server
  rad pair [--lan] [--print-url] show a QR code to pair a phone
  rad devices [revoke <id>]      list or revoke paired devices
  rad debug <cmd> ...            CLI client for testing (run "rad debug" for help)
  rad install-launchagent        run rad at login (macOS)
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
	case "install-launchagent":
		err = installLaunchAgent(os.Args[2:])
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
	lan := fs.Bool("lan", false, "also listen on LAN addresses")
	verbose := fs.Bool("v", false, "debug logging")
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

	orch := orchestrator.New(st, buildRegistry(cfg), orchestrator.Options{
		WorktreesDir: cfg.WorktreesDir(), LogsDir: cfg.LogsDir(), IdleTimeout: cfg.IdleTimeout.Duration, Log: log,
	})
	if err := orch.Recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	go orch.Run(ctx)

	serverID, err := st.ServerID(ctx)
	if err != nil {
		return err
	}
	srv := api.NewServer(st, hub, orch, fsbrowse.New(cfg.Roots), serverID, netinfo.Hostname(), log)
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	addrs := netinfo.ListenAddrs(cfg)
	errc := make(chan error, len(addrs))
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			return fmt.Errorf("listen %s: %w", a, err)
		}
		log.Info("listening", "addr", "http://"+a)
		go func() { errc <- httpSrv.Serve(ln) }()
	}
	log.Info("rad ready", "config", cfg.ConfigPath, "data", cfg.DataDir, "roots", cfg.Roots)
	if devs, _ := st.Devices(ctx); len(devs) == 0 {
		fmt.Fprintln(os.Stderr, "\nNo devices paired yet.")
		printPairing(ctx, st, cfg, false)
	}

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

func pair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	printURL := fs.Bool("print-url", false, "print only the pairing link")
	lan := fs.Bool("lan", false, "include LAN addresses (for a server started with serve --lan)")
	fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.LAN = cfg.LAN || *lan
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	return printPairing(context.Background(), st, cfg, *printURL)
}

const pairingTTL = 10 * time.Minute

func printPairing(ctx context.Context, st *store.Store, cfg *config.Config, urlOnly bool) error {
	code, err := st.CreatePairingCode(ctx, pairingTTL)
	if err != nil {
		return err
	}
	link := netinfo.PairingLink(netinfo.Hostname(), code, netinfo.BaseURLs(netinfo.ListenAddrs(cfg)))
	if urlOnly {
		fmt.Println(link)
		return nil
	}
	fmt.Fprintln(os.Stderr, "\nScan with the Remote Agent app (valid for 10 minutes, single use):")
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stderr, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 2,
	})
	fmt.Fprintf(os.Stderr, "Or paste this link in the app:\n%s\n\n", link)
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
