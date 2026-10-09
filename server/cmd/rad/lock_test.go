package main

import (
	"errors"
	"testing"
)

func TestLockDataDir(t *testing.T) {
	dir := t.TempDir()
	first, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks belong to the open file, so a second open in this process
	// conflicts just like a second rad would.
	var busy *busyError
	if _, err := lockDataDir(dir); !errors.As(err, &busy) {
		t.Fatalf("second lock: got %v, want busyError", err)
	}
	first.Close()
	again, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Close()
}
