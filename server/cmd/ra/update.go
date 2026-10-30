package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"remote-agent/internal/update"
)

// updateRa replaces this ra with the latest release's, as `rad update`
// replaces rad: the release's ra-<os>-<arch>.tar.gz, checked against its
// SHA256SUMS, run once, then renamed into place, the old one kept as
// ra.old.
func updateRa(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report whether a newer release is out")
	force := fs.Bool("force", false, "install the latest release over a development build, or again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return selfUpdate(context.Background(), update.For("ra", version), version, exe, *check, *force, out)
}

func selfUpdate(ctx context.Context, u *update.Client, current, exe string, check, force bool, out io.Writer) error {
	rel, err := u.Latest(ctx)
	if err != nil {
		return fmt.Errorf("look up the latest release: %w", err)
	}
	dev := !update.IsRelease(current)
	newer := !dev && update.Compare(rel.Version, current) > 0
	if check {
		switch {
		case dev:
			fmt.Fprintf(out, "ra %s is a development build; the latest release is %s: %s\n", current, rel.Version, rel.URL)
		case newer:
			fmt.Fprintf(out, "ra %s is available (this is %s): %s\n", rel.Version, current, rel.URL)
		default:
			fmt.Fprintf(out, "ra %s is up to date\n", current)
		}
		return nil
	}
	if strings.Contains(exe, "go-build") {
		return errors.New("this is a temporary `go run` binary; there is nothing to update")
	}
	switch {
	case dev && !force:
		return fmt.Errorf("ra %s is a development build; `ra update --force` replaces it with %s", current, rel.Version)
	case !dev && !newer && !force:
		fmt.Fprintf(out, "ra %s is up to date\n", current)
		return nil
	}
	fmt.Fprintf(out, "downloading ra %s (%s)\n", rel.Version, u.Asset())
	old := exe + ".old"
	if err := update.KeepCopy(exe, old); err != nil {
		return writeHint(err, exe)
	}
	if err := u.Install(ctx, rel, exe); err != nil {
		return writeHint(err, exe)
	}
	fmt.Fprintf(out, "installed ra %s at %s; the previous version is %s\n", rel.Version, exe, old)
	return nil
}

func writeHint(err error, exe string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w\n\nra can't replace %s. Run ra update as the user who owns it, or reinstall ra in a folder you own", err, exe)
	}
	return err
}
