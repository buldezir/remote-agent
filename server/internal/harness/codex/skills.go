package codex

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

type skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Enabled     *bool  `json:"enabled"`
	Interface   *struct {
		ShortDescription string `json:"shortDescription"`
	} `json:"interface"`
}

// skills lists the workspace's skills; refresh has Codex read them from disk
// again. A successful lookup becomes the catalog that Prompt resolves from.
func (r *runtime) skills(ctx context.Context, refresh bool) ([]skill, error) {
	var res struct {
		Data []struct {
			Cwd    string  `json:"cwd"`
			Skills []skill `json:"skills"`
		} `json:"data"`
	}
	if err := r.conn.Call(ctx, "skills/list", map[string]any{"cwds": []string{r.cwd}, "forceReload": refresh}, &res); err != nil {
		return nil, err
	}
	var out []skill
	seen := map[string]bool{}
	for _, group := range res.Data {
		if filepath.Clean(group.Cwd) != filepath.Clean(r.cwd) && resolvedPath(group.Cwd) != resolvedPath(r.cwd) {
			continue
		}
		for _, s := range group.Skills {
			if (s.Enabled != nil && !*s.Enabled) || s.Name == "" || s.Path == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	r.mu.Lock()
	r.skillCatalog, r.skillsLoaded = out, true
	r.mu.Unlock()
	return out, nil
}

func (r *runtime) Commands(ctx context.Context) ([]model.Command, error) {
	skills, err := r.skills(ctx, true)
	if err != nil {
		return nil, err
	}
	out := []model.Command{harness.Compact}
	for _, s := range skills {
		description := s.Description
		if s.Interface != nil && s.Interface.ShortDescription != "" {
			description = s.Interface.ShortDescription
		}
		out = append(out, model.Command{Name: s.Name, Description: description, Kind: "skill", Scope: s.Scope})
	}
	return out, nil
}

// skillInput resolves only a leading explicit invocation, preserving arguments and images.
func skillInput(text string, skills []skill) (string, map[string]any) {
	if !invokes(text) {
		return text, nil
	}
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, s := range skills {
		for _, prefix := range []string{"/", "$"} {
			rest, ok := strings.CutPrefix(trimmed, prefix+s.Name)
			if ok && (rest == "" || unicode.IsSpace([]rune(rest)[0])) {
				return "$" + s.Name + rest, map[string]any{"type": "skill", "name": s.Name, "path": s.Path}
			}
		}
	}
	return text, nil
}

// invokes reports whether text starts the way a skill invocation does.
func invokes(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "$")
}

func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
