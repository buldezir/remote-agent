// Command ra is Remote Agent's terminal client: the apps' sessions, prompts
// and approvals for the rad on this machine.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"remote-agent/internal/client"
	"remote-agent/internal/tui"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

const usage = `ra — Remote Agent in the terminal, for the rad on this machine

Usage:
  ra [--theme system|dark|light] [--layout inbox|projects|columns]
                       open it; the first time, it pairs with rad
  ra pair [<link>]     pair again; without a link it asks this machine's rad
                       (rad pair --print-url)
  ra unpair            revoke this device on rad, and forget it
  ra version

--theme and --layout try a setting without saving it.
`

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ra:", err)
		os.Exit(1)
	}
}

// dirs are where ra keeps its config and its cache: RA_HOME, or the XDG
// folders, apart from rad's ~/.config/remote-agent.
func dirs() (config, cache string, err error) {
	if h := os.Getenv("RA_HOME"); h != "" {
		return h, filepath.Join(h, "cache"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	config = filepath.Join(home, ".config", "ra")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		config = filepath.Join(x, "ra")
	}
	cache = filepath.Join(home, ".cache", "ra")
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		cache = filepath.Join(x, "ra")
	} else if c, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(c, "ra")
	}
	return config, cache, nil
}

func run(args []string) error {
	config, cache, err := dirs()
	if err != nil {
		return err
	}
	store := client.NewServerStore(config)
	if len(args) > 0 {
		switch args[0] {
		case "pair":
			return pair(store, args[1:], os.Stdout)
		case "unpair":
			return unpair(store, os.Stdout)
		case "version", "--version", "-v":
			fmt.Println("ra", version)
			return nil
		case "help", "-h", "--help":
			fmt.Print(usage)
			return nil
		}
	}
	fs := flag.NewFlagSet("ra", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	theme := fs.String("theme", "", "")
	layout := fs.String("layout", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
	switch *theme {
	case "", "system", "dark", "light":
	default:
		return fmt.Errorf("--theme is system, dark or light")
	}
	switch *layout {
	case "", "inbox", "projects", "columns":
	default:
		return fmt.Errorf("--layout is inbox, projects or columns")
	}

	saved, token, ok := store.Load()
	if !ok {
		fmt.Println("Pairing with the rad on this machine…")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		saved, token, err = client.PairLocal(ctx, store)
		cancel()
		if err != nil {
			return pairingError(err)
		}
	}
	q := client.NewQueue()
	conn := client.NewServerConnection(q, saved.URLs, token, store.Drafts())
	conn.Images = client.NewImageCache(filepath.Join(cache, "images"))
	conn.OnURLWorked = store.Promote
	conn.Reauthorize = func(ctx context.Context) (string, []string, error) {
		s, tok, err := client.PairLocal(ctx, store)
		if err != nil {
			return "", nil, pairingError(err)
		}
		return tok, s.URLs, nil
	}
	err = tui.Run(tui.Options{Queue: q, Conn: conn, Store: store, Dir: config, Version: version, Theme: *theme, Layout: *layout})
	conn.Stop()
	q.Drain()
	return err
}

func pairingError(err error) error {
	switch {
	case errors.Is(err, client.ErrNoRad):
		return fmt.Errorf(`%w.
To pair with a rad elsewhere on this machine, run: ra pair "$(/path/to/rad pair --print-url)"`, err)
	case client.ErrorCode(err) == "unreachable":
		return fmt.Errorf("%w\nIs rad running? Start it with `rad serve`, or open the Remote Agent Server app", err)
	}
	return err
}

func pair(store *client.ServerStore, args []string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var saved client.SavedServer
	var err error
	if len(args) > 0 {
		link, ok := client.ParsePairingLink(args[0])
		if !ok {
			return fmt.Errorf("%q isn't a pairing link: run `rad pair --print-url` for one", args[0])
		}
		saved, _, err = client.PairWith(ctx, store, link)
	} else {
		saved, _, err = client.PairLocal(ctx, store)
	}
	if err != nil {
		return pairingError(err)
	}
	fmt.Fprintf(out, "Paired with %s (%s) as %q.\n", saved.Name, saved.URLs[0], client.DeviceName())
	return nil
}

func unpair(store *client.ServerStore, out io.Writer) error {
	saved, token, ok := store.Load()
	if !ok {
		fmt.Fprintln(out, "ra isn't paired.")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := client.NewServerConnection(client.NewQueue(), saved.URLs, token, nil)
	if err := conn.Unpair(ctx); err != nil {
		fmt.Fprintf(out, "Couldn't revoke this device on rad (%v); forgetting it here. `rad devices` lists it.\n", err)
	}
	conn.RPC().Disconnect("", false)
	if err := store.Forget(); err != nil {
		return err
	}
	fmt.Fprintf(out, "Unpaired from %s.\n", saved.Name)
	return nil
}
