package main

import (
	"os/exec"
	"strings"
	"testing"
)

// ra mustn't link rad's server: its database, its API or its orchestrator.
func TestDoesNotLinkTheServer(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skip("go list:", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch {
		case strings.HasPrefix(dep, "modernc.org/sqlite"),
			dep == "remote-agent/internal/store",
			dep == "remote-agent/internal/api",
			dep == "remote-agent/internal/orchestrator",
			strings.HasPrefix(dep, "remote-agent/internal/harness"):
			t.Errorf("ra links %s", dep)
		}
	}
}
