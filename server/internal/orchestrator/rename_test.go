package orchestrator

import (
	"strings"
	"testing"

	"remote-agent/internal/model"
)

func (e *env) rename(id, title string) *model.Session {
	e.t.Helper()
	s, err := e.o.Rename(bg, id, title)
	if err != nil {
		e.t.Fatalf("rename %s: %v", id, err)
	}
	return s
}

func TestRename(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Prompt: "slow naming please", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitTurn(s.ID, 1)
	before := e.session(s.ID)

	// The user's title, while the system agent is still at it: it stays,
	// and the branch is named as usual.
	got := e.rename(s.ID, "  Release\tchecklist\n")
	if got.Title != "Release checklist" || !got.CustomTitle || !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("renamed = %q, custom %v, updated %v (was %v)", got.Title, got.CustomTitle, got.UpdatedAt, before.UpdatedAt)
	}
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != "Release checklist" || !got.CustomTitle || got.Workspace.Branch != "ra/named-slow-naming-please" {
		t.Fatalf("after naming: %q, custom %v, on %q", got.Title, got.CustomTitle, got.Workspace.Branch)
	}
	// Later prompts leave it as it is.
	e.prompt(s.ID, "and b.txt", "")
	e.waitTurn(s.ID, 2)
	if got := e.session(s.ID); got.Title != "Release checklist" {
		t.Fatalf("after a prompt: %q", got.Title)
	}
	// Long titles are cut.
	if got := e.rename(s.ID, strings.Repeat("a", 200)); len([]rune(got.Title)) != 120 || !strings.HasSuffix(got.Title, "…") {
		t.Fatalf("long title = %q", got.Title)
	}

	// An empty title gives it back to rad: the first prompt's, then the
	// system agent's, and the branch stays.
	got = e.rename(s.ID, "")
	if got.Title != "slow naming please" || got.CustomTitle {
		t.Fatalf("automatic = %q, custom %v", got.Title, got.CustomTitle)
	}
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != "Named: slow naming please" || got.Workspace.Branch != "ra/named-slow-naming-please" {
		t.Fatalf("named again: %q on %q", got.Title, got.Workspace.Branch)
	}
	if strings.Contains(e.log.String(), "Release") {
		t.Fatal("the log has the title")
	}
	if _, err := e.o.Rename(bg, "missing", "x"); CodeOf(err) != CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestRenameKeepsTitleFromDrafts(t *testing.T) {
	e := newEnv(t, false)
	e.setSystemAgent("fake")
	// "New session" as the user's own title is theirs too.
	s := e.create(CreateSessionParams{})
	e.rename(s.ID, "New session")
	if _, err := e.o.SaveDraft(bg, s.ID, "tidy the docs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.o.SaveDraft(bg, s.ID, "", nil); err != nil {
		t.Fatal(err)
	}
	e.prompt(s.ID, "tidy the docs", "")
	e.waitTurn(s.ID, 1)
	e.o.naming.Wait()
	if got := e.session(s.ID); got.Title != "New session" || !got.CustomTitle {
		t.Fatalf("after a draft and a prompt: %q, custom %v", got.Title, got.CustomTitle)
	}

	// Back to rad's with only a draft: the draft names it.
	d := e.create(CreateSessionParams{Title: "Mine", Prompt: "plan the move", Draft: true})
	if !d.CustomTitle {
		t.Fatal("a title given at create isn't the user's")
	}
	if got := e.rename(d.ID, ""); got.Title != "plan the move" || got.CustomTitle {
		t.Fatalf("automatic = %q", got.Title)
	}
	if got := e.named(d.ID, "plan the move"); got.Title != "Named: plan the move" {
		t.Fatalf("named %q", got.Title)
	}
	// With nothing at all, the default title, which the next prompt replaces.
	n := e.create(CreateSessionParams{Title: "Empty"})
	if got := e.rename(n.ID, ""); got.Title != "New session" {
		t.Fatalf("automatic without a prompt = %q", got.Title)
	}
	e.prompt(n.ID, "tidy up", "")
	if got := e.named(n.ID, "tidy up"); got.Title != "Named: tidy up" {
		t.Fatalf("named %q", got.Title)
	}
}

func TestRenameForksAndImports(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	src := e.create(CreateSessionParams{Prompt: "make a.txt"})
	e.waitTurn(src.ID, 1)
	e.o.naming.Wait()
	e.rename(src.ID, "Payments")

	// A fork of a session the user named keeps the name, and the system
	// agent doesn't name it from the fork's prompt.
	f := e.fork(ForkParams{SessionID: src.ID, Prompt: "now b.txt"})
	if f.Title != "Payments (fork)" || !f.CustomTitle {
		t.Fatalf("fork = %q, custom %v", f.Title, f.CustomTitle)
	}
	e.waitTurn(f.ID, 2)
	e.o.naming.Wait()
	if got := e.session(f.ID); got.Title != "Payments (fork)" {
		t.Fatalf("fork named %q", got.Title)
	}
	// Back to rad's, a fork's is its source's with " (fork)".
	e.rename(src.ID, "Billing")
	if got := e.rename(f.ID, ""); got.Title != "Billing (fork)" || got.CustomTitle {
		t.Fatalf("fork's automatic = %q, custom %v", got.Title, got.CustomTitle)
	}

	// An archived session can be renamed.
	if err := e.o.Archive(bg, src.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := e.rename(src.ID, "Billing, done"); got.Title != "Billing, done" || !got.Archived {
		t.Fatalf("archived = %q, archived %v", got.Title, got.Archived)
	}

	// Imports: a title given is the user's; the agent's own isn't.
	list := e.importable("fake")
	mine, err := e.o.Import(bg, ImportParams{ProjectID: e.proj.ID, Harness: "fake", NativeID: list[0].ID, Title: "Dark theme"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := e.o.Import(bg, ImportParams{ProjectID: e.proj.ID, Harness: "fake", NativeID: list[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	e.o.naming.Wait()
	if got := e.session(mine.ID); got.Title != "Dark theme" || !got.CustomTitle {
		t.Fatalf("imported with a title: %q, custom %v", got.Title, got.CustomTitle)
	}
	if got := e.session(theirs.ID); got.Title != "Fix the login redirect" || got.CustomTitle {
		t.Fatalf("imported with the agent's title: %q, custom %v", got.Title, got.CustomTitle)
	}
}
