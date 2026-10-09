package pi

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"remote-agent/internal/model"
)

// describeTool returns the kind, a one-line title and the file paths of a
// call to one of pi's tools: read, bash, edit, write, grep, find and ls, plus
// whatever extensions add.
func describeTool(name string, args json.RawMessage, cwd string) (model.ToolKind, string, []string) {
	var in map[string]any
	json.Unmarshal(args, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	dirs := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		dirs = append(dirs, real)
	}
	rel := func(p string) string {
		for _, d := range dirs {
			if r, err := filepath.Rel(d, p); d != "" && filepath.IsAbs(p) && err == nil && !strings.HasPrefix(r, "..") {
				return r
			}
		}
		return p
	}
	path := rel(str("path"))
	if path == "" {
		path = rel(str("file_path"))
	}
	switch name {
	case "bash":
		return model.ToolExecute, firstLine(str("command")), nil
	case "read":
		return model.ToolRead, "Read " + path, []string{path}
	case "write":
		return model.ToolEdit, "Write " + path, []string{path}
	case "edit", "multi_edit", "multiedit":
		return model.ToolEdit, "Edit " + path, []string{path}
	case "grep":
		s := "Grep " + str("pattern")
		if path != "" {
			s += " in " + path
		}
		return model.ToolSearch, s, nil
	case "find":
		return model.ToolSearch, "Find " + str("pattern"), nil
	case "ls":
		if path == "" {
			path = "."
		}
		return model.ToolSearch, "List " + path, nil
	}
	kind := model.ToolOther
	if strings.Contains(name, "web") || strings.Contains(name, "fetch") || strings.Contains(name, "search") {
		kind = model.ToolFetch
	}
	if path != "" {
		return kind, name + ": " + path, []string{path}
	}
	for _, k := range []string{"description", "pattern", "url", "query", "command"} {
		if s := str(k); s != "" {
			return kind, name + ": " + firstLine(s), nil
		}
	}
	return kind, name, nil
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
