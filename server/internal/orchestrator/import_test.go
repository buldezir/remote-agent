package orchestrator

import (
	"strings"
	"testing"

	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
)

// The fake agent ran two sessions of its own in every folder, as if in a
// terminal: terminal-1, "Fix the login redirect", and terminal-2, unnamed,
// which it wrote to just now (fake/import.go).

func (e *env) importable(harnessID string) []model.ImportableSession {
	e.t.Helper()
	list, err := e.o.Importable(bg, e.proj.ID, harnessID)
	if err != nil {
		e.t.Fatalf("importable: %v", err)
	}
	return list
}

func TestImport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	list := e.importable("fake")
	if len(list) != 2 || !strings.HasPrefix(list[0].ID, "terminal-2-") || !list[0].Active ||
		list[1].Title != "Fix the login redirect" || list[1].Active || list[1].Messages != 4 {
		t.Fatalf("importable = %+v", list)
	}
	native := list[1].ID

	s, err := e.o.Import(bg, ImportParams{CommandID: "i1", ProjectID: e.proj.ID, Harness: "fake", NativeID: native, Mode: "auto", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Fix the login redirect" || s.NativeID != native || s.Harness != "fake" || s.Mode != "auto" || s.Effort != "high" ||
		s.Workspace.Kind != model.WorkspaceRoot || s.Workspace.Path != e.dir || s.Workspace.Branch != "main" ||
		s.ModelInfo == nil || s.ModelInfo.ID != "echo" || s.Status != model.SessionIdle {
		t.Errorf("session = %+v", s)
	}
	turns, items := e.turns(s.ID), e.items(s.ID)
	if len(turns) != 2 || turns[0].Status != model.TurnCompleted || turns[1].ForkPoint != native+"#2" || turns[0].EndedAt == nil {
		t.Errorf("turns = %+v", turns)
	}
	if got := kinds(items); got != "user_message,tool_call,assistant_message,user_message,tool_call,assistant_message,notice" {
		t.Errorf("items = %s", got)
	}
	checkOrder(t, items, turns)
	if n := items[len(items)-1]; n.TurnID != "" || !strings.HasPrefix(n.Text, "Picked up from Fake (scripted)") || strings.Contains(n.Text, "minute") {
		t.Errorf("notice = %+v", n)
	}
	if items[0].Text != "Fix the login redirect" || items[0].TurnID != turns[0].ID || items[0].CreatedAt.After(*turns[0].EndedAt) {
		t.Errorf("first item = %+v", items[0])
	}

	// A retry gets the same session; another import of it is refused, and
	// the listing leaves it out.
	if again, err := e.o.Import(bg, ImportParams{CommandID: "i1", ProjectID: e.proj.ID, Harness: "fake", NativeID: native}); err != nil || again.ID != s.ID {
		t.Errorf("retry = %+v, %v", again, err)
	}
	if _, err := e.o.Import(bg, ImportParams{CommandID: "i2", ProjectID: e.proj.ID, Harness: "fake", NativeID: native}); CodeOf(err) != CodeConflict {
		t.Errorf("second import: %v", err)
	}
	if list := e.importable("fake"); len(list) != 1 || list[0].ID == native {
		t.Errorf("importable after = %+v", list)
	}

	// The next prompt resumes the agent's own session, as turn 3.
	e.prompt(s.ID, "carry on", "")
	if turn := e.waitTurn(s.ID, 3); turn.Status != model.TurnCompleted {
		t.Fatalf("turn 3 = %+v", turn)
	}
	if reply := e.lastReply(s.ID); !strings.HasPrefix(reply, "Picked up "+native+". Echo: carry on") {
		t.Errorf("reply = %q", reply)
	}
	if got := e.session(s.ID); got.NativeID != native {
		t.Errorf("native id = %q", got.NativeID)
	}
	checkOrder(t, e.items(s.ID), e.turns(s.ID))

	// Its turns fork as rad's own do: the agent copies its conversation.
	f := e.fork(ForkParams{CommandID: "f1", SessionID: s.ID, Turn: 1, Prompt: "again"})
	e.waitTurn(f.ID, 2)
	if reply := e.lastReply(f.ID); !strings.HasPrefix(reply, "Forked "+native+" at "+native+"#1.") {
		t.Errorf("fork's reply = %q", reply)
	}
}

func TestImportActiveAndNamed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.setSystemAgent("fake")
	native := e.importable("fake")[0].ID
	s, err := e.o.Import(bg, ImportParams{ProjectID: e.proj.ID, Harness: "fake", NativeID: native})
	if err != nil {
		t.Fatal(err)
	}
	// Without a name of its own, the first prompt names it, and then the
	// system agent.
	if s.Title != "Add a dark theme to the settings page" || s.Mode != "ask" || s.Workspace.Branch != "" {
		t.Errorf("session = %+v", s)
	}
	items := e.items(s.ID)
	if n := items[len(items)-1]; !strings.Contains(n.Text, "less than a minute ago") {
		t.Errorf("notice = %q, want the warning", n.Text)
	}
	e.o.naming.Wait()
	if got := e.session(s.ID).Title; got != "Named: Add a dark theme" {
		t.Errorf("named %q", got)
	}
}

func TestImportErrors(t *testing.T) {
	t.Parallel()
	e := newEnvAt(t, t.TempDir(), false, fake.Harness{}, plainAgent{})
	for _, c := range []struct {
		p    ImportParams
		code string
	}{
		{ImportParams{ProjectID: e.proj.ID, Harness: "fake", NativeID: "terminal-9-00000000"}, CodeNotFound},
		{ImportParams{ProjectID: e.proj.ID, Harness: "fake"}, CodeInvalid},
		{ImportParams{ProjectID: e.proj.ID, Harness: "plain", NativeID: "x"}, CodeInvalid},
		{ImportParams{ProjectID: e.proj.ID, Harness: "nope", NativeID: "x"}, CodeInvalid},
		{ImportParams{ProjectID: "nope", Harness: "fake", NativeID: "x"}, CodeNotFound},
	} {
		if _, err := e.o.Import(bg, c.p); CodeOf(err) != c.code {
			t.Errorf("Import(%+v) = %v, want %s", c.p, err, c.code)
		}
	}
	if _, err := e.o.Importable(bg, e.proj.ID, "plain"); CodeOf(err) != CodeInvalid {
		t.Errorf("Importable(plain) = %v", err)
	}
}
