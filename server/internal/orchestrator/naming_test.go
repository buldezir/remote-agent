package orchestrator

import (
	"strings"
	"testing"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
)

func (e *env) setSystemAgent(harness string) {
	e.t.Helper()
	if _, err := e.o.SetSettings(bg, model.ServerSettings{SystemAgent: &model.AgentSpec{Harness: harness, Model: "echo"}}); err != nil {
		e.t.Fatal(err)
	}
}

// named waits for the session's title to change from the one it has.
func (e *env) named(id, from string) *model.Session {
	e.t.Helper()
	var s *model.Session
	e.waitFor("the session to be named", func() bool {
		s = e.session(id)
		return s.Title != from
	})
	return s
}

func TestNamingWithoutSystemAgent(t *testing.T) {
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Prompt: "tidy notes.txt please", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitTurn(s.ID, 1)
	e.o.naming.Wait()
	got := e.session(s.ID)
	if got.Title != "tidy notes.txt please" || !strings.HasPrefix(got.Workspace.Branch, "ra/tidy-notes-txt-please-") {
		t.Fatalf("session %q on %q", got.Title, got.Workspace.Branch)
	}
}

func TestNamingTitleAndBranch(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Prompt: "Fix the login loop on Safari", Mode: "auto", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	if s.Title != "Fix the login loop on Safari" || !isProvisional(s.Workspace.Branch, s.ID) {
		t.Fatalf("created as %q on %q", s.Title, s.Workspace.Branch)
	}
	got := e.named(s.ID, s.Title)
	e.o.naming.Wait()
	got = e.session(s.ID)
	if got.Title != "Named: Fix the login loop" || got.Workspace.Branch != "ra/named-fix-the-login-loop" {
		t.Fatalf("named %q on %q", got.Title, got.Workspace.Branch)
	}
	if b := gitx.CurrentBranch(bg, got.Workspace.Path); b != got.Workspace.Branch {
		t.Fatalf("worktree is on %q", b)
	}
	if gitx.BranchExists(bg, e.dir, s.Workspace.Branch) {
		t.Fatalf("the provisional branch %s is still there", s.Workspace.Branch)
	}
	// The turn goes on in the renamed branch, and the next turn sees it.
	e.waitTurn(s.ID, 1)
	e.prompt(s.ID, "write b.txt", "")
	e.waitTurn(s.ID, 2)
	if b := e.session(s.ID).Workspace.Branch; b != "ra/named-fix-the-login-loop" {
		t.Fatalf("after another turn on %q", b)
	}
	if strings.Contains(e.log.String(), "Named:") {
		t.Fatal("the log has the title")
	}

	// The same name again: the branch is taken, so it gets the session's suffix.
	s2 := e.create(CreateSessionParams{Prompt: "Fix the login loop on Safari", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.named(s2.ID, s2.Title)
	e.o.naming.Wait()
	if b := e.session(s2.ID).Workspace.Branch; b != "ra/named-fix-the-login-loop-"+branchSuffix(s2.ID) {
		t.Fatalf("second session on %q", b)
	}
}

func TestNamingKeepsChosenNames(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	// A title given: only the branch is named.
	s := e.create(CreateSessionParams{Title: "Mine", Prompt: "tidy the readme", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitFor("the branch to be named", func() bool { return e.session(s.ID).Workspace.Branch == "ra/named-tidy-the-readme" })
	if got := e.session(s.ID); got.Title != "Mine" {
		t.Fatalf("title %q", got.Title)
	}
	// A branch given: only the title.
	s = e.create(CreateSessionParams{Prompt: "tidy the docs", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "my-branch"}})
	if got := e.named(s.ID, s.Title); got.Title != "Named: tidy the docs" || got.Workspace.Branch != "my-branch" {
		t.Fatalf("named %q on %q", got.Title, got.Workspace.Branch)
	}
	// In the project's folder there's no branch to name.
	s = e.create(CreateSessionParams{Prompt: "look around"})
	if got := e.named(s.ID, s.Title); got.Title != "Named: look around" || got.Workspace.Branch != "main" {
		t.Fatalf("named %q on %q", got.Title, got.Workspace.Branch)
	}
}

func TestNamingAtFirstPrompt(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	if s.Title != "New session" || !strings.HasPrefix(s.Workspace.Branch, "ra/new-session-") {
		t.Fatalf("created as %q on %q", s.Title, s.Workspace.Branch)
	}
	e.prompt(s.ID, "add a changelog", "")
	got := e.named(s.ID, "add a changelog")
	e.o.naming.Wait()
	got = e.session(s.ID)
	if got.Title != "Named: add a changelog" || got.Workspace.Branch != "ra/named-add-a-changelog" {
		t.Fatalf("named %q on %q", got.Title, got.Workspace.Branch)
	}
	// Later prompts don't rename it.
	e.waitTurn(s.ID, 1)
	e.prompt(s.ID, "and a license", "")
	e.waitTurn(s.ID, 2)
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != "Named: add a changelog" {
		t.Fatalf("renamed to %q", got.Title)
	}
}

func TestNamingFromDraft(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	// A draft names the session as its first prompt would, and nothing runs.
	s := e.create(CreateSessionParams{Prompt: "add a changelog", Draft: true, Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.named(s.ID, s.Title)
	e.o.naming.Wait()
	got := e.session(s.ID)
	if got.Title != "Named: add a changelog" || got.Workspace.Branch != "ra/named-add-a-changelog" {
		t.Fatalf("named %q on %q", got.Title, got.Workspace.Branch)
	}
	if n := len(e.turns(s.ID)); n != 0 {
		t.Fatalf("turns = %d", n)
	}
	// Sending it doesn't name it again.
	if _, err := e.o.SendDraft(bg, s.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(s.ID, 1)
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != "Named: add a changelog" {
		t.Fatalf("renamed to %q", got.Title)
	}

	// Saved in a session started empty.
	empty := e.create(CreateSessionParams{})
	if _, err := e.o.SaveDraft(bg, empty.ID, "write the docs", nil); err != nil {
		t.Fatal(err)
	}
	if got := e.named(empty.ID, "write the docs"); got.Title != "Named: write the docs" {
		t.Fatalf("named %q", got.Title)
	}
}

func TestNamingFailureKeepsNames(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Prompt: "this will fail to name", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.o.naming.Wait()
	got := e.session(s.ID)
	if got.Title != s.Title || got.Workspace.Branch != s.Workspace.Branch {
		t.Fatalf("now %q on %q", got.Title, got.Workspace.Branch)
	}
	if !strings.Contains(e.log.String(), "naming the session failed") {
		t.Fatalf("log: %s", e.log.String())
	}
}

func TestNamingArchivedSession(t *testing.T) {
	e := newEnv(t, false)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Prompt: "slow naming please"})
	e.waitTurn(s.ID, 1)
	if err := e.o.Archive(bg, s.ID, false); err != nil {
		t.Fatal(err)
	}
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != s.Title {
		t.Fatalf("archived session renamed to %q", got.Title)
	}
}

func TestSetSettings(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.o.SetSettings(bg, model.ServerSettings{SystemAgent: &model.AgentSpec{Harness: "nope"}}); err == nil {
		t.Fatal("unknown harness accepted")
	}
	got, err := e.o.SetSettings(bg, model.ServerSettings{SystemAgent: &model.AgentSpec{Harness: " fake ", Model: " echo "}})
	if err != nil || *got.SystemAgent != (model.AgentSpec{Harness: "fake", Model: "echo"}) {
		t.Fatalf("set %+v, %v", got.SystemAgent, err)
	}
	if s, _ := e.o.Settings(bg); s.SystemAgent == nil || s.SystemAgent.Model != "echo" {
		t.Fatalf("stored %+v", s.SystemAgent)
	}

	if _, err := e.o.SetSettings(bg, model.ServerSettings{DefaultAgent: &model.AgentSpec{Harness: "nope"}}); err == nil {
		t.Fatal("unknown default agent accepted")
	}
	got, err = e.o.SetSettings(bg, model.ServerSettings{DefaultAgent: &model.AgentSpec{Harness: "fake ", Model: "echo", Effort: " high"}})
	if err != nil || got.SystemAgent != nil || *got.DefaultAgent != (model.AgentSpec{Harness: "fake", Model: "echo", Effort: "high"}) {
		t.Fatalf("set %+v, %v", got, err)
	}
	// It replaces them all: the system agent is gone.
	if s, _ := e.o.Settings(bg); s.SystemAgent != nil || s.DefaultAgent == nil || s.DefaultAgent.Effort != "high" {
		t.Fatalf("stored %+v", s)
	}
}

func TestParseNames(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  names
	}{
		{`{"title": "Fix login", "branch": "fix-login"}`, names{"Fix login", "fix-login"}},
		{"Sure!\n```json\n{\"title\": \"\\\"Fix login.\\\"\", \"branch\": \"fix/Login Redirect_loop\"}\n```", names{"Fix login", "login-redirect-loop"}},
		{`{"title": "Réparer la connexion", "branch": "réparer-connexion"}`, names{"Réparer la connexion", "r-parer-connexion"}},
		{`{"title": "x", "branch": "` + strings.Repeat("word-", 20) + `"}`, names{"x", strings.TrimSuffix(strings.Repeat("word-", 8), "-")}},
	} {
		got, err := parseNames(tc.reply)
		if err != nil || got != tc.want {
			t.Errorf("parseNames(%q) = %+v, %v; want %+v", tc.reply, got, err, tc.want)
		}
	}
	for _, bad := range []string{"no json", `{"title": ""}`, `{"title": 3}`} {
		if _, err := parseNames(bad); err == nil {
			t.Errorf("parseNames(%q) succeeded", bad)
		}
	}
}

func TestNamingPrompt(t *testing.T) {
	p := namingPrompt("web", strings.Repeat("é", maxRequest+10), true)
	if !strings.Contains(p, `"branch"`) || !strings.Contains(p, `The project is "web"`) || strings.Count(p, "é") != maxRequest {
		t.Fatalf("prompt: %s", p)
	}
	if strings.Contains(namingPrompt("", "hi", false), `"branch"`) {
		t.Fatal("asks for a branch it doesn't need")
	}
}

func TestTurnSignsAgentOut(t *testing.T) {
	e := newEnv(t, false)
	h, _ := e.o.reg.Get("fake")
	s := e.create(CreateSessionParams{Prompt: "logout"})
	e.waitTurn(s.ID, 1)
	if info := e.o.reg.Info(bg, h, false); info.AuthOK || info.Hint != "Signed out: Invalid API key · Please run /login" {
		t.Fatalf("after a failed login: %+v", info)
	}
	e.prompt(s.ID, "hello again", "")
	e.waitTurn(s.ID, 2)
	if info := e.o.reg.Info(bg, h, false); !info.AuthOK {
		t.Fatalf("after a turn that worked: %+v", info)
	}
}
