package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

func render(chunks ...string) *screen {
	s := newScreen(rows)
	for _, c := range chunks {
		s.Write([]byte(c))
	}
	return s
}

func TestScreen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"lines", []string{"one\r\ntwo\r\n"}, "one\ntwo"},
		{"blank lines", []string{"\r\na\r\n\r\n\r\nb\r\n\r\n"}, "\na\n\n\nb"},
		{"colours", []string{"\x1b[94mblue\x1b[0m and \x1b[1;31mred\x1b[m"}, "blue and red"},
		{"carriage return redraws", []string{"Downloading  50%\r", "Downloading 100%\r\ndone"}, "Downloading 100%\ndone"},
		{"erase line", []string{"spinner |", "\r\x1b[2Kfinished"}, "finished"},
		{"cursor up redraws", []string{"a\r\nb\r\n", "\x1b[2A\x1b[2KA\r\n\x1b[2KB\r\n"}, "A\nB"},
		{"clear screen", []string{"old\r\n\x1b[2J\x1b[Hnew"}, "new"},
		{"private modes", []string{"\x1b[?25lhidden cursor\x1b[?25h"}, "hidden cursor"},
		{"utf-8 split", []string{"caf\xc3", "\xa9 …"}, "café …"},
		{"charset", []string{"\x1b(Bplain"}, "plain"},
		{"hyperlink", []string{"\x1b]8;;https://a.example/x\x07link\x1b]8;;\x07 here"}, "link here"},
		{"tab", []string{"a\tb"}, "a       b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.chunks...).Text(); got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScreenURL(t *testing.T) {
	for _, tc := range []struct {
		name, out, want string
	}{
		{"plain", "visit: https://claude.com/oauth?code=true&x=1\r\nPaste code here > ", "https://claude.com/oauth?code=true&x=1"},
		{"last wins", "https://a.example/1 then https://b.example/2.", "https://b.example/2"},
		{"brackets", "(see https://a.example/x)", "https://a.example/x"},
		{"balanced", "https://en.wikipedia.org/wiki/Go_(language)", "https://en.wikipedia.org/wiki/Go_(language)"},
		{"hyperlink only", "\x1b]8;;https://a.example/link\x07Sign in\x1b]8;;\x07", "https://a.example/link"},
		{"web links only", "\x1b]8;;file:///etc/passwd\x07x\x1b]8;;\x07 \x1b]8;;javascript:alert(1)\x07y\x1b]8;;\x07", ""},
		{"none", "nothing here", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.out).URL(); got != tc.want {
				t.Errorf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScreenKeepsLastLines(t *testing.T) {
	s := newScreen(rows)
	for i := range maxLines + 50 {
		s.Write([]byte(strings.Repeat("x", i%7) + "\r\n"))
	}
	s.Write([]byte("last"))
	lines := strings.Split(s.Text(), "\n")
	if len(lines) > maxLines || lines[len(lines)-1] != "last" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func wait(t *testing.T, m *Manager, id string, done func(model.Task) bool) model.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if done(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("task: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ended(s model.Task) bool { return s.Status != model.TaskRunning }

func sh(script string) harness.Command {
	return harness.Command{Args: []string{"/bin/sh", "-c", script}}
}

func TestRunWithInput(t *testing.T) {
	m := NewManager(nil)
	defer m.Shutdown()
	task := m.Start(model.TaskLogin, "fake", func(ctx context.Context, t *Task) error {
		err := t.Run(ctx, sh(`echo "Open https://example.com/login?x=1 to sign in"; printf "Code > "; read code; echo "got $code"; [ "$code" = 42 ]`))
		t.Println("rad: done")
		return err
	})
	if task.Status != model.TaskRunning || task.Kind != model.TaskLogin || task.Harness != "fake" {
		t.Fatalf("started %+v", task)
	}
	s := wait(t, m, task.ID, func(s model.Task) bool { return strings.Contains(s.Output, "Code >") })
	if !s.Input || s.URL != "https://example.com/login?x=1" {
		t.Fatalf("waiting for input: %+v", s)
	}
	if err := m.Input(task.ID, "42\n"); err != nil {
		t.Fatal(err)
	}
	s = wait(t, m, task.ID, ended)
	if s.Status != model.TaskSucceeded || s.Input || s.EndedAt == nil || !strings.Contains(s.Output, "got 42") || !strings.HasSuffix(s.Output, "rad: done") {
		t.Fatalf("ended %+v", s)
	}
	if err := m.Input(task.ID, "again"); err == nil {
		t.Fatal("input after the end")
	}
}

func TestRunFailsAndCancels(t *testing.T) {
	m := NewManager(nil)
	defer m.Shutdown()
	failing := m.Start(model.TaskUpdateAgent, "a", func(ctx context.Context, t *Task) error { return t.Run(ctx, sh("echo oops; exit 3")) })
	if s := wait(t, m, failing.ID, ended); s.Status != model.TaskFailed || !strings.Contains(s.Error, "exit status 3") || s.Output != "oops" {
		t.Fatalf("failed task %+v", s)
	}

	slow := m.Start(model.TaskUpdateAgent, "b", func(ctx context.Context, t *Task) error { return t.Run(ctx, sh("echo started; sleep 30")) })
	// A second start while it runs returns it.
	if again := m.Start(model.TaskUpdateAgent, "b", nil); again.ID != slow.ID {
		t.Fatalf("second start made %s, not %s", again.ID, slow.ID)
	}
	wait(t, m, slow.ID, func(s model.Task) bool { return s.Output == "started" })
	if err := m.Cancel(slow.ID); err != nil {
		t.Fatal(err)
	}
	if s := wait(t, m, slow.ID, ended); s.Status != model.TaskCancelled {
		t.Fatalf("cancelled task %+v", s)
	}
	if got := m.List(""); len(got) != 2 || got[0].ID != failing.ID {
		t.Fatalf("list %+v", got)
	}
	if _, err := m.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
}

func TestSessionTasks(t *testing.T) {
	m := NewManager(nil)
	defer m.Shutdown()
	update := m.Start(model.TaskUpdateServer, "", func(ctx context.Context, t *Task) error {
		t.Println("See https://example.com/notes")
		return nil
	})
	release := make(chan struct{})
	commit := m.StartForSession(model.TaskCommit, "s1", func(ctx context.Context, t *Task) error {
		t.Step(model.TaskStep{ID: "commit", Title: "Commit", Status: model.StepRunning})
		t.Println("Visit https://example.com/pull/new/x to open one")
		<-release
		t.Step(model.TaskStep{ID: "commit", Title: "Commit", Status: model.StepSucceeded, Detail: "abc1234 Fix"})
		t.Step(model.TaskStep{ID: "push", Title: "Push", Status: model.StepSucceeded})
		t.SetURL("https://example.com/pull/1")
		return nil
	})
	if commit.SessionID != "s1" || commit.Harness != "" {
		t.Fatalf("started %+v", commit)
	}
	// One per session: another start returns it, another session gets its own.
	if again := m.StartForSession(model.TaskCommit, "s1", nil); again.ID != commit.ID {
		t.Fatalf("second start made %s, not %s", again.ID, commit.ID)
	}
	other := m.StartForSession(model.TaskCommit, "s2", func(ctx context.Context, t *Task) error { return nil })
	if other.ID == commit.ID {
		t.Fatal("another session's commit is the first's")
	}
	// Only what the task sets is its url: not the output's.
	s := wait(t, m, commit.ID, func(s model.Task) bool { return len(s.Steps) == 1 && s.Steps[0].Status == model.StepRunning })
	if s.URL != "" {
		t.Fatalf("url %q from the output", s.URL)
	}
	close(release)
	s = wait(t, m, commit.ID, ended)
	if s.Status != model.TaskSucceeded || s.URL != "https://example.com/pull/1" || len(s.Steps) != 2 || s.Steps[0].Detail != "abc1234 Fix" {
		t.Fatalf("ended %+v", s)
	}
	if u := wait(t, m, update.ID, ended); u.URL != "https://example.com/notes" {
		t.Fatalf("update's url %q", u.URL)
	}
	// The server's tasks and a session's are listed apart.
	if got := m.List(""); len(got) != 1 || got[0].ID != update.ID {
		t.Fatalf("server's tasks %+v", got)
	}
	if got := m.List("s1"); len(got) != 1 || got[0].ID != commit.ID {
		t.Fatalf("session's tasks %+v", got)
	}
}

func TestShutdownCancels(t *testing.T) {
	m := NewManager(nil)
	task := m.Start(model.TaskUpdateServer, "", func(ctx context.Context, t *Task) error {
		<-ctx.Done()
		return ctx.Err()
	})
	m.Shutdown()
	if s, _ := m.Get(task.ID); s.Status != model.TaskCancelled {
		t.Fatalf("after shutdown %+v", s)
	}
}

func TestEnviron(t *testing.T) {
	env := environ([]string{"PATH=/bin", "TERM=screen", "ITERM_PROFILE=x", "CLAUDECODE=1", "BROWSER=firefox", "KEEP=1"},
		harness.Command{Env: map[string]string{"KEY": "v"}, Unset: []string{"CLAUDECODE"}})
	got := strings.Join(env, " ")
	for _, want := range []string{"PATH=/bin", "KEEP=1", "TERM=xterm-256color", "BROWSER=true", "NO_OPEN_BROWSER=1", "KEY=v"} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %s: %s", want, got)
		}
	}
	for _, gone := range []string{"screen", "ITERM_PROFILE", "CLAUDECODE", "firefox"} {
		if strings.Contains(got, gone) {
			t.Errorf("env has %s: %s", gone, got)
		}
	}
}
