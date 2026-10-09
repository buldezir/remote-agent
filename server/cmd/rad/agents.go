package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

const noAgents = "install Claude Code, Codex, Pi or an ACP agent (see acp in the config)"

// agentStatus splits the installed agents into ready ones and ones that need
// attention, such as a login. Agents that aren't installed are left out.
func agentStatus(infos []model.HarnessInfo) (ready, notReady []model.HarnessInfo) {
	for _, i := range infos {
		switch {
		case !i.Installed:
		case i.AuthOK:
			ready = append(ready, i)
		default:
			notReady = append(notReady, i)
		}
	}
	return ready, notReady
}

// agentNames lists agents as "Claude Code 2.1.295, Codex 0.161.0".
func agentNames(infos []model.HarnessInfo) string {
	names := make([]string, len(infos))
	for n, i := range infos {
		names[n] = strings.TrimSpace(i.Name + " " + i.Version)
	}
	return strings.Join(names, ", ")
}

// logAgents probes the agents once at startup and returns what it found. This
// also fills the probe cache, so the phone's first look at the agent list is
// quick.
func logAgents(ctx context.Context, log *slog.Logger, reg *harness.Registry) []model.HarnessInfo {
	infos := reg.Infos(ctx, false)
	ready, notReady := agentStatus(infos)
	if len(ready) > 0 {
		log.Info("agents ready", "agents", agentNames(ready))
	} else {
		log.Warn("no agents ready: " + noAgents)
	}
	for _, i := range notReady {
		log.Warn("agent not ready", "agent", i.Name, "hint", i.Hint)
	}
	return infos
}

func printAgents(ctx context.Context, w io.Writer, reg *harness.Registry) {
	ready, notReady := agentStatus(reg.Infos(ctx, false))
	if len(ready) > 0 {
		fmt.Fprintf(w, "Agents ready: %s\n", agentNames(ready))
	} else {
		fmt.Fprintf(w, "No agents ready: %s.\n", noAgents)
	}
	for _, i := range notReady {
		fmt.Fprintf(w, "Not ready: %s (%s)\n", i.Name, i.Hint)
	}
}
