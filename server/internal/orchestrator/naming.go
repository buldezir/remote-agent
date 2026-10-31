package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// The system agent names a session from its first prompt: a title, and a
// branch for its worktree. It also writes commit messages and pull requests
// (commit.go). Until it answers, the session has the names rad
// makes up (titleFrom, provisionalBranch), and it keeps them if the agent
// fails. Names the user chose are left alone.

const namingTimeout = 90 * time.Second

// maxRequest is how much of the prompt the system agent reads.
const maxRequest = 2000

// Settings returns the settings clients set.
func (o *Orchestrator) Settings(ctx context.Context) (model.ServerSettings, error) {
	return o.st.Settings(ctx)
}

// SetSettings checks and saves them.
func (o *Orchestrator) SetSettings(ctx context.Context, s model.ServerSettings) (model.ServerSettings, error) {
	if a := s.SystemAgent; a != nil {
		a.Harness, a.Model, a.Effort = strings.TrimSpace(a.Harness), strings.TrimSpace(a.Model), strings.TrimSpace(a.Effort)
		h, ok := o.reg.Get(a.Harness)
		if !ok {
			return s, errorf(CodeInvalid, "unknown harness %q", a.Harness)
		}
		if _, ok := h.(harness.OneShotter); !ok {
			return s, errorf(CodeInvalid, "%s can't be the system agent", a.Harness)
		}
		if info := o.reg.Info(ctx, h, false); !info.Installed {
			return s, errorf(CodeUnavailable, "%s is not installed on this machine", info.Name)
		}
	}
	return s, o.st.SetSettings(ctx, s)
}

// provisionalBranch is a worktree's branch until the system agent names it.
func provisionalBranch(title, sessionID string) string {
	return "ra/" + slug(title) + "-" + branchSuffix(sessionID)
}

func branchSuffix(sessionID string) string { return sessionID[len(sessionID)-6:] }

// isProvisional reports whether a branch is one rad made up for the session.
func isProvisional(branch, sessionID string) bool {
	return strings.HasPrefix(branch, "ra/") && strings.HasSuffix(branch, "-"+branchSuffix(sessionID))
}

// nameSession has the system agent, if one is set, name a session from its
// first prompt, in the background: its title, if title, and its worktree's
// branch, if branch.
func (o *Orchestrator) nameSession(sessionID, projectName, text string, title, branch bool) {
	if strings.TrimSpace(text) == "" || !title && !branch {
		return
	}
	o.naming.Go(func() {
		ctx, cancel := context.WithTimeout(o.bg, namingTimeout)
		defer cancel()
		settings, err := o.st.Settings(ctx)
		if err != nil || settings.SystemAgent == nil {
			return
		}
		agent := *settings.SystemAgent
		start := time.Now()
		names, err := o.askNames(ctx, agent, projectName, text, branch)
		if err != nil {
			if o.bg.Err() == nil {
				o.log.Warn("naming the session failed", "session", sessionID, "harness", agent.Harness, "model", agent.Model, "err", err)
			}
			return
		}
		if !title {
			names.Title = ""
		}
		if !branch {
			names.Branch = ""
		}
		// Not for a session archived or deleted meanwhile, which has no actor to load.
		if s, err := o.st.Session(ctx, sessionID); err != nil || s.Archived {
			return
		}
		a, err := o.actor(ctx, sessionID)
		if err != nil {
			return
		}
		if a.applyNames(ctx, names) {
			o.log.Info("session named", "session", sessionID, "harness", agent.Harness, "model", agent.Model,
				"took", time.Since(start).Round(time.Millisecond))
		}
	})
}

type names struct {
	Title  string `json:"title"`
	Branch string `json:"branch"`
}

// askNames has the system agent name the session.
func (o *Orchestrator) askNames(ctx context.Context, agent model.SystemAgent, projectName, text string, branch bool) (names, error) {
	reply, err := o.oneShot(ctx, agent, namingPrompt(projectName, text, branch))
	if err != nil {
		return names{}, err
	}
	return parseNames(reply)
}

// oneShot runs the system agent once, in an empty folder of its own.
func (o *Orchestrator) oneShot(ctx context.Context, agent model.SystemAgent, prompt string) (string, error) {
	h, ok := o.reg.Get(agent.Harness)
	if !ok {
		return "", fmt.Errorf("unknown harness %q", agent.Harness)
	}
	one, ok := h.(harness.OneShotter)
	if !ok {
		return "", fmt.Errorf("%s can't be the system agent", agent.Harness)
	}
	dir, err := os.MkdirTemp("", "rad-system-agent-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	return one.OneShot(ctx, harness.OneShotOptions{Cwd: dir, Model: agent.Model, Effort: agent.Effort, Prompt: prompt})
}

func namingPrompt(projectName, text string, branch bool) string {
	if utf8.RuneCountInString(text) > maxRequest {
		text = string([]rune(text)[:maxRequest]) + "…"
	}
	var b strings.Builder
	b.WriteString("You name the sessions of a coding agent. Don't do the task, and don't use tools. Reply with only a JSON object:\n")
	b.WriteString(`{"title": "<what the session is about, in at most 6 words, in the request's language, sentence case, without quotes or a full stop>"`)
	if branch {
		b.WriteString(`, "branch": "<a git branch name for the work: 2 to 5 lowercase English words joined by hyphens>"`)
	}
	b.WriteString("}\n\n")
	if projectName != "" {
		fmt.Fprintf(&b, "The project is %q. ", projectName)
	}
	b.WriteString("The session's first request:\n<request>\n")
	b.WriteString(text)
	b.WriteString("\n</request>\n")
	return b.String()
}

// parseNames reads the JSON object in the agent's reply, which may come
// with words around it or in a code block.
func parseNames(reply string) (names, error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return names{}, errors.New("the reply has no JSON object")
	}
	var n names
	if err := json.Unmarshal([]byte(reply[start:end+1]), &n); err != nil {
		return names{}, fmt.Errorf("the reply's JSON: %w", err)
	}
	n.Title = cleanTitle(n.Title)
	n.Branch = branchSlug(n.Branch)
	if n.Title == "" && n.Branch == "" {
		return names{}, errors.New("the reply names nothing")
	}
	return n, nil
}

// userTitle is a title the user gave: on one line, without the spaces
// around it, and at most 120 characters. "" asks for rad's.
func userTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 120 {
		s = string([]rune(s)[:119]) + "…"
	}
	return s
}

func cleanTitle(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(strings.Trim(s, "\"'`“”‘’*"))
	s = strings.TrimSuffix(s, ".")
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:79]) + "…"
	}
	return s
}

// branchSlug keeps a branch name to lowercase ASCII words joined by hyphens,
// at most 40 characters, cut between words. A prefix such as "fix/" goes.
func branchSlug(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	for len(out) > 40 {
		i := strings.LastIndex(out[:40], "-")
		if i <= 0 {
			out = out[:40]
			break
		}
		out = out[:i]
	}
	return out
}

// applyNames gives the session the names, where it still has the ones rad
// made up: not a title the user chose meanwhile. A branch that is taken gets
// the session's suffix. It reports whether anything changed.
func (a *actor) applyNames(ctx context.Context, n names) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted || a.sess.Archived {
		return false
	}
	changed := false
	if n.Title != "" && n.Title != a.sess.Title && !a.sess.CustomTitle {
		a.sess.Title = n.Title
		changed = true
	}
	ws := a.sess.Workspace
	if n.Branch != "" && ws.Kind == model.WorkspaceWorktree && isProvisional(ws.Branch, a.sess.ID) &&
		gitx.CurrentBranch(ctx, ws.Path) == ws.Branch {
		for _, name := range []string{"ra/" + n.Branch, "ra/" + n.Branch + "-" + branchSuffix(a.sess.ID)} {
			if gitx.BranchExists(ctx, ws.Path, name) {
				continue
			}
			if err := gitx.RenameBranch(ctx, ws.Path, ws.Branch, name); err != nil {
				a.o.log.Warn("renaming the session's branch failed", "session", a.sess.ID, "err", err)
				break
			}
			a.sess.Workspace.Branch = name
			changed = true
			break
		}
	}
	if changed {
		a.write(ctx, a.putSession)
	}
	return changed
}
