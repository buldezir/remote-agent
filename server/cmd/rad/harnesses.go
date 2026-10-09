package main

import (
	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/acp"
	"remote-agent/internal/harness/claude"
	"remote-agent/internal/harness/codex"
)

// harnessesFor builds the real harness adapters from config.
func harnessesFor(cfg *config.Config) []harness.Harness {
	hs := []harness.Harness{
		claude.New(cfg.HarnessCmd("claude")),
		codex.New(cfg.HarnessCmd("codex")),
	}
	for _, a := range cfg.ACP {
		hs = append(hs, acp.New(a))
	}
	return hs
}
