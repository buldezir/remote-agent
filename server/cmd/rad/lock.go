package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// exitBusy is rad's exit code when another rad already serves the same data
// dir or port (EX_TEMPFAIL). The Mac app reads it as "running elsewhere" and
// doesn't restart rad.
const exitBusy = 75

// busyError means another rad is already running.
type busyError struct{ msg string }

func (e *busyError) Error() string { return e.msg }

// lockDataDir takes an exclusive lock on dir/rad.lock that lasts as long as
// the returned file stays open, so two servers never share one database.
func lockDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "rad.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &busyError{"another rad is already running with " + dir}
		}
		return nil, err
	}
	return f, nil
}
