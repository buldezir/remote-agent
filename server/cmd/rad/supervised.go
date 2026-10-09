package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"

	"remote-agent/internal/model"
)

// The Remote Agent Server Mac app runs `rad serve --supervised` as its child,
// so macOS attributes rad and its agents to the app when asking for
// permissions. The app holds rad's stdin open and reads one JSON object per
// line from its stdout; the log still goes to stderr.

// readyStatus is written once rad is listening.
type readyStatus struct {
	Event         string   `json:"event"` // "ready"
	Version       string   `json:"version"`
	URLs          []string `json:"urls"`
	Listen        []string `json:"listen"`
	Config        string   `json:"config"`
	Data          string   `json:"data"`
	PairedDevices int      `json:"pairedDevices"`
}

// agentsStatus follows once the agents have been probed.
type agentsStatus struct {
	Event  string              `json:"event"` // "agents"
	Agents []model.HarnessInfo `json:"agents"`
}

// supervise returns a context that ends when stdin closes, which happens when
// the app quits or dies, and the encoder for the status lines.
func supervise(ctx context.Context) (context.Context, *json.Encoder) {
	// A closed stdout must not kill rad before it has stopped its agents.
	signal.Ignore(syscall.SIGPIPE)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	return ctx, json.NewEncoder(os.Stdout)
}
