package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"remote-agent/internal/api"
	"remote-agent/internal/update"
)

// installRa puts ra, the terminal client, next to this rad, or in --dir:
// from this rad's own release, so that the two match, or for a
// development build, from the latest. From then on `ra update` keeps it
// current.
func installRa(args []string) error {
	fs := flag.NewFlagSet("install-ra", flag.ExitOnError)
	dir := fs.String("dir", "", "the folder to put ra in (default: this rad's)")
	force := fs.Bool("force", false, "install ra again, or over a newer one or another program called ra")
	fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return installRaFor(context.Background(), update.For("ra", api.Version), api.Version, exe, *dir, *force, os.Stdout)
}

func installRaFor(ctx context.Context, u *update.Client, version, exe, dir string, force bool, out io.Writer) error {
	if dir == "" {
		if strings.Contains(exe, "go-build") {
			return errors.New("this is a temporary `go run` binary; pass --dir to say where ra goes")
		}
		if app := appBundle(exe); app != "" {
			return fmt.Errorf("this rad is part of %s, which ra can't go into; pass --dir, such as --dir ~/.local/bin", filepath.Base(app))
		}
		dir = filepath.Dir(exe)
	} else {
		dir = expandHome(dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var rel *update.Release
	var err error
	if update.IsRelease(version) {
		rel, err = u.Release(ctx, version)
	} else {
		fmt.Fprintf(out, "rad %s is a development build; ra comes from the latest release\n", version)
		rel, err = u.Latest(ctx)
	}
	if err != nil {
		return fmt.Errorf("look up the release: %w", err)
	}

	dst := filepath.Join(dir, "ra")
	if _, err := os.Stat(dst); err == nil && !force {
		have, isRa := raVersion(ctx, dst)
		switch {
		case !isRa:
			return fmt.Errorf("%s isn't ra (it doesn't answer `ra version`); --force replaces it", tilde(dst))
		case !update.IsRelease(have):
			return fmt.Errorf("%s is a development build of ra (%s); --force replaces it with %s", tilde(dst), have, rel.Version)
		case update.Compare(have, rel.Version) == 0:
			fmt.Fprintf(out, "ra %s is already at %s\n", have, tilde(dst))
			return nil
		case update.Compare(have, rel.Version) > 0:
			fmt.Fprintf(out, "ra %s at %s is newer than this rad's release, %s; `ra update` keeps it current, and --force puts %s there\n", have, tilde(dst), rel.Version, rel.Version)
			return nil
		}
	}

	fmt.Fprintf(out, "downloading ra %s (%s)\n", rel.Version, u.Asset())
	old := ""
	if _, err := os.Stat(dst); err == nil {
		old = dst + ".old"
		if err := update.KeepCopy(dst, old); err != nil {
			return installHint(err, dir)
		}
	}
	if err := u.Install(ctx, rel, dst); err != nil {
		return installHint(err, dir)
	}
	fmt.Fprintf(out, "installed ra %s at %s", rel.Version, tilde(dst))
	if old != "" {
		fmt.Fprintf(out, "; the previous one is %s", tilde(old))
	}
	fmt.Fprintln(out)
	if onPath(dir) {
		fmt.Fprintln(out, "run `ra` to start it; `ra update` installs new releases")
	} else {
		fmt.Fprintf(out, "%s isn't on your PATH: run %s, or add the folder to PATH\n", tilde(dir), tilde(dst))
	}
	return nil
}

// raVersion asks the ra at path its version.
func raVersion(ctx context.Context, path string) (version string, isRa bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", false
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ra ")
	return v, ok
}

// onPath reports whether dir is one of PATH's folders.
func onPath(dir string) bool {
	want := canonical(dir)
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" && canonical(d) == want {
			return true
		}
	}
	return false
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func installHint(err error, dir string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w\n\nrad can't write to %s. Run rad install-ra as the user who owns it, or pass --dir with a folder you own, such as ~/.local/bin", err, tilde(dir))
	}
	return err
}
