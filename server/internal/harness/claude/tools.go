package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"remote-agent/internal/model"
)

func toolKind(name string) model.ToolKind {
	switch name {
	case "Read", "NotebookRead", "LS":
		return model.ToolRead
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return model.ToolEdit
	case "Bash", "BashOutput", "KillShell", "PowerShell":
		return model.ToolExecute
	case "Glob", "Grep", "ToolSearch":
		return model.ToolSearch
	case "WebFetch", "WebSearch":
		return model.ToolFetch
	case "Task", "Agent":
		return model.ToolThink
	}
	return model.ToolOther
}

// describeTool returns a one-line title and the file paths a tool call touches.
func describeTool(name string, input json.RawMessage, cwd string) (string, []string) {
	var in map[string]any
	json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	// Claude reports resolved paths, e.g. /private/tmp/… for a cwd under /tmp on macOS.
	dirs := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		dirs = append(dirs, real)
	}
	rel := func(p string) string {
		for _, d := range dirs {
			if r, err := filepath.Rel(d, p); d != "" && err == nil && !strings.HasPrefix(r, "..") {
				return r
			}
		}
		return p
	}
	// Titles show paths outside the cwd under the home folder as ~/…, such as Claude's plan files.
	home, _ := os.UserHomeDir()
	short := func(p string) string {
		r := rel(p)
		if h, err := filepath.Rel(home, r); home != "" && filepath.IsAbs(r) && err == nil && !strings.HasPrefix(h, "..") {
			return filepath.Join("~", h)
		}
		return r
	}
	switch name {
	case "Bash", "PowerShell":
		cmd := firstLine(str("command"))
		if d := str("description"); d != "" && len(cmd) > 80 {
			return d, nil
		}
		return cmd, nil
	case "Read", "Write", "Edit", "MultiEdit":
		p := str("file_path")
		verb := map[string]string{"Read": "Read", "Write": "Write", "Edit": "Edit", "MultiEdit": "Edit"}[name]
		return verb + " " + short(p), []string{rel(p)}
	case "NotebookEdit":
		p := str("notebook_path")
		return "Edit " + short(p), []string{rel(p)}
	case "Glob":
		return "Glob " + str("pattern"), nil
	case "Grep":
		s := "Grep " + str("pattern")
		if p := str("path"); p != "" {
			s += " in " + short(p)
		}
		return s, nil
	case "WebFetch":
		return "Fetch " + str("url"), nil
	case "WebSearch":
		return "Search " + str("query"), nil
	case "Task", "Agent":
		if d := str("description"); d != "" {
			return "Agent: " + d, nil
		}
	}
	for _, k := range []string{"description", "file_path", "path", "pattern", "url", "query", "command"} {
		if s := str(k); s != "" {
			return name + ": " + firstLine(s), nil
		}
	}
	return name, nil
}

func firstLine(s string) string {
	line, _, more := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 160 {
		line = line[:160] + "…"
	} else if more {
		line += " …"
	}
	return line
}
