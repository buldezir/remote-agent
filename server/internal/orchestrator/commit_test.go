package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/tasks"
)

// The tests of committing never reach a real remote or GitHub: origin is a
// bare repository in a temporary folder, and gh is a script first on PATH,
// which logs its arguments.

const fakeGH = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
if [ -n "$FAKE_GH_SIGNED_OUT" ]; then
  echo "To get started with GitHub CLI, please run:  gh auth login" >&2
  exit 4
fi
case "$1 $2" in
"pr list")
  if [ -n "$FAKE_GH_OPEN" ]; then printf '[{"number":3,"url":"%s","title":"Older"}]\n' "$FAKE_GH_OPEN"; else echo '[]'; fi ;;
"pr create")
  cat > "$FAKE_GH_LOG.body"
  echo "Creating pull request into main in test/repo" >&2
  echo "https://github.com/test/repo/pull/12" ;;
*)
  echo "fake gh: unknown command $*" >&2
  exit 1 ;;
esac
`

// withFakeGH puts the fake gh first on PATH, and returns the file it logs
// its calls to. The test can't be parallel.
func withFakeGH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("FAKE_GH_OPEN", "")
	t.Setenv("FAKE_GH_SIGNED_OUT", "")
	return log
}

// withoutGH leaves only git on PATH.
func withoutGH(t *testing.T) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func ghCalls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// addOrigin makes a bare repository the project's origin, with main pushed
// to it, and returns its folder.
func (e *env) addOrigin() string {
	e.t.Helper()
	origin := filepath.Join(e.t.TempDir(), "origin.git")
	run(e.t, e.dir, "git", "init", "-q", "--bare", "-b", "main", origin)
	run(e.t, e.dir, "git", "remote", "add", "origin", origin)
	run(e.t, e.dir, "git", "push", "-q", "origin", "main")
	run(e.t, e.dir, "git", "fetch", "-q", "origin")
	return origin
}

// commit runs a commit as rad does, as a task, and waits for it to end.
func (e *env) commit(p CommitParams) model.Task {
	e.t.Helper()
	if err := e.o.CheckCommit(bg, p.SessionID); err != nil {
		e.t.Fatalf("check: %v", err)
	}
	m := tasks.NewManager(nil)
	e.t.Cleanup(m.Shutdown)
	started := m.StartForSession(model.TaskCommit, p.SessionID, func(ctx context.Context, t *tasks.Task) error {
		return e.o.Commit(ctx, p, t)
	})
	var task model.Task
	e.waitFor("the commit to end", func() bool {
		task, _ = m.Get(started.ID)
		return task.Status != model.TaskRunning
	})
	return task
}

func steps(task model.Task) string {
	var out []string
	for _, s := range task.Steps {
		out = append(out, s.ID+":"+string(s.Status))
	}
	return strings.Join(out, " ")
}

func (e *env) notices(sessionID string) []string {
	var out []string
	for _, it := range byKind(e.items(sessionID), model.ItemNotice) {
		out = append(out, it.Text)
	}
	return out
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPushAndPullRequest(t *testing.T) {
	ghLog := withFakeGH(t)
	e := newEnv(t, true)
	origin := e.addOrigin()
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Title: "Edit the files", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/feature"}})
	dir := s.Workspace.Path
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "b.txt", "new\n")
	run(t, dir, "git", "add", "a.txt") // the user's index, which a preview leaves as it is

	p, err := e.o.CommitPreview(bg, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Branch != "ra/feature" || p.Remote != "origin" || p.Base != "main" || p.Ahead != 0 || len(p.Files) != 2 ||
		p.Files[0].Path != "a.txt" || p.Files[0].Status != gitx.Modified || p.Files[1].Path != "b.txt" || p.Files[1].Status != gitx.Added {
		t.Fatalf("preview %+v", p)
	}
	if p.Message != "Change a.txt, b.txt\n\nWritten by the fake system agent." {
		t.Fatalf("message %q", p.Message)
	}
	if staged := run(t, dir, "git", "diff", "--cached", "--name-only"); staged != "a.txt" {
		t.Fatalf("the index has %q", staged)
	}

	task := e.commit(CommitParams{SessionID: s.ID, Message: "Edit the files\n\nBy hand.  \n\n\n", PullRequest: true})
	if task.Status != model.TaskSucceeded || steps(task) != "commit:succeeded push:succeeded pull_request:succeeded" {
		t.Fatalf("task %+v", task)
	}
	if task.Steps[1].Title != "Push to origin" || task.Steps[1].Detail != "ra/feature → origin" ||
		task.Steps[2].Detail != "#12 Fake: Edit the files" || task.URL != "https://github.com/test/repo/pull/12" {
		t.Fatalf("steps %+v, url %q", task.Steps, task.URL)
	}
	if msg := run(t, origin, "git", "log", "-1", "--format=%B", "ra/feature"); msg != "Edit the files\n\nBy hand." {
		t.Fatalf("origin's commit %q", msg)
	}
	sha := run(t, dir, "git", "rev-parse", "--short=7", "HEAD")
	if task.Steps[0].Detail != sha+" Edit the files" {
		t.Fatalf("commit step %+v", task.Steps[0])
	}
	if up := run(t, dir, "git", "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/ra/feature" {
		t.Fatalf("upstream %q", up)
	}
	if gitx.Dirty(bg, dir) {
		t.Fatal("left changes uncommitted")
	}
	for _, want := range []string{"$ git add -A", "$ git commit -F -", "$ git push --set-upstream origin ra/feature",
		"$ gh pr create --head ra/feature --title 'Fake: Edit the files' --body-file - --base main",
		// gh's stderr, then its stdout, as it wrote them.
		"Creating pull request into main in test/repo\nhttps://github.com/test/repo/pull/12"} {
		if !strings.Contains(task.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, task.Output)
		}
	}
	calls := ghCalls(t, ghLog)
	if len(calls) != 2 || calls[0] != "pr list --head ra/feature --state open --json number,url,title --limit 1" ||
		calls[1] != "pr create --head ra/feature --title Fake: Edit the files --body-file - --base main" {
		t.Fatalf("gh calls %q", calls)
	}
	if body, _ := os.ReadFile(ghLog + ".body"); string(body) != "Commits:\n\n- Edit the files" {
		t.Fatalf("body %q", body)
	}
	want := "Committed " + sha + " “Edit the files”, pushed ra/feature to origin and opened pull request https://github.com/test/repo/pull/12"
	if n := e.notices(s.ID); len(n) != 1 || n[0] != want {
		t.Fatalf("notices %q", n)
	}
	a, _ := e.o.actor(bg, s.ID)
	if !strings.Contains(a.note, "the user committed "+sha) {
		t.Fatalf("note for the agent %q", a.note)
	}

	// Again, with nothing to commit: the push brings the branch up to date,
	// and the pull request is open already.
	write(t, dir, "c.txt", "more\n")
	run(t, dir, "git", "add", "c.txt")
	run(t, dir, "git", "commit", "-q", "-m", "Add c")
	if p, _ := e.o.CommitPreview(bg, s.ID); len(p.Files) != 0 || p.Message != "" || p.Ahead != 1 {
		t.Fatalf("second preview %+v", p)
	}
	t.Setenv("FAKE_GH_OPEN", "https://github.com/test/repo/pull/3")
	task = e.commit(CommitParams{SessionID: s.ID, PullRequest: true})
	if task.Status != model.TaskSucceeded || steps(task) != "commit:skipped push:succeeded pull_request:succeeded" ||
		task.Steps[0].Detail != "Nothing to commit" || task.Steps[2].Detail != "Already open: #3 Older" || task.URL != "https://github.com/test/repo/pull/3" {
		t.Fatalf("second task %+v", task)
	}
	if got := run(t, origin, "git", "log", "-1", "--format=%s", "ra/feature"); got != "Add c" {
		t.Fatalf("origin has %q", got)
	}
	if calls := ghCalls(t, ghLog); len(calls) != 3 || !strings.HasPrefix(calls[2], "pr list") {
		t.Fatalf("gh calls %q", calls)
	}
}

func TestCommitBranchFromRemote(t *testing.T) {
	withFakeGH(t)
	e := newEnv(t, true)
	origin := e.addOrigin()
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/x", BaseRef: "origin/main"}})
	dir := s.Workspace.Path
	// Made from origin/main, the branch tracks main: a push goes to a branch
	// of its own.
	if up := run(t, dir, "git", "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/main" {
		t.Fatalf("upstream %q", up)
	}
	write(t, dir, "a.txt", "changed\n")
	p, err := e.o.CommitPreview(bg, s.ID)
	if err != nil || p.Remote != "origin" || p.Base != "main" || p.Message != "Update a.txt" {
		t.Fatalf("preview %+v, %v", p, err)
	}
	task := e.commit(CommitParams{SessionID: s.ID, PullRequest: true})
	if task.Status != model.TaskSucceeded {
		t.Fatalf("task %+v", task)
	}
	if got := run(t, origin, "git", "log", "-1", "--format=%s", "main"); got != "init" {
		t.Fatalf("origin's main moved to %q", got)
	}
	if got := run(t, origin, "git", "log", "-1", "--format=%s", "ra/x"); got != "Update a.txt" {
		t.Fatalf("origin's ra/x is %q", got)
	}
	if !strings.Contains(task.Output, "--base main") {
		t.Fatalf("output %s", task.Output)
	}
}

func TestCommitWithoutRemote(t *testing.T) {
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Title: "Fix the redirect", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/fix"}})
	dir := s.Workspace.Path
	write(t, dir, "a.txt", "fixed\n")
	p, err := e.o.CommitPreview(bg, s.ID)
	if err != nil || p.Remote != "" || p.Base != "main" || p.Message != "Fix the redirect" || p.Ahead != 0 {
		t.Fatalf("preview %+v, %v", p, err)
	}

	task := e.commit(CommitParams{SessionID: s.ID})
	if task.Status != model.TaskSucceeded || steps(task) != "commit:succeeded" || task.URL != "" {
		t.Fatalf("commit only %+v", task)
	}
	if got := run(t, dir, "git", "log", "-1", "--format=%s"); got != "Fix the redirect" {
		t.Fatalf("committed %q", got)
	}

	task = e.commit(CommitParams{SessionID: s.ID, Push: true})
	if task.Status != model.TaskFailed || steps(task) != "commit:skipped push:failed" || task.Steps[1].Title != "Push" ||
		!strings.Contains(task.Error, "no remote to push to") || task.Steps[1].Detail != task.Error {
		t.Fatalf("push %+v", task)
	}
	if n := e.notices(s.ID); len(n) != 1 || !strings.HasPrefix(n[0], "Committed ") {
		t.Fatalf("notices %q", n)
	}

	run(t, e.dir, "git", "remote", "add", "fork", t.TempDir())
	task = e.commit(CommitParams{SessionID: s.ID, Push: true})
	if task.Status != model.TaskFailed || !strings.Contains(task.Error, "no remote named origin (it has fork)") {
		t.Fatalf("push without origin %+v", task)
	}
}

func TestPullRequestWithoutGH(t *testing.T) {
	e := newEnv(t, true)
	origin := e.addOrigin()
	withoutGH(t)
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/x"}})
	write(t, s.Workspace.Path, "b.txt", "new\n")
	task := e.commit(CommitParams{SessionID: s.ID, PullRequest: true})
	if task.Status != model.TaskFailed || steps(task) != "commit:succeeded push:succeeded pull_request:failed" ||
		!strings.Contains(task.Error, "gh, isn't installed") || task.URL != "" {
		t.Fatalf("task %+v", task)
	}
	if got := run(t, origin, "git", "log", "-1", "--format=%s", "ra/x"); got != "Update b.txt" {
		t.Fatalf("origin's ra/x is %q", got)
	}
	if n := e.notices(s.ID); len(n) != 1 || !strings.Contains(n[0], "and pushed ra/x to origin.") {
		t.Fatalf("notices %q", n)
	}
}

func TestPullRequestSignedOut(t *testing.T) {
	ghLog := withFakeGH(t)
	t.Setenv("FAKE_GH_SIGNED_OUT", "1")
	e := newEnv(t, true)
	e.addOrigin()
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/x"}})
	write(t, s.Workspace.Path, "b.txt", "new\n")
	task := e.commit(CommitParams{SessionID: s.ID, PullRequest: true})
	if task.Status != model.TaskFailed || steps(task) != "commit:succeeded push:succeeded pull_request:failed" ||
		!strings.Contains(task.Error, "gh isn't signed in to GitHub") {
		t.Fatalf("task %+v", task)
	}
	if calls := ghCalls(t, ghLog); len(calls) != 1 {
		t.Fatalf("gh calls %q", calls)
	}
}

func TestPullRequestFromDefaultBranch(t *testing.T) {
	ghLog := withFakeGH(t)
	e := newEnv(t, true)
	e.addOrigin()
	run(t, e.dir, "git", "remote", "set-head", "origin", "main")
	s := e.create(CreateSessionParams{})
	write(t, e.dir, "a.txt", "changed\n")
	task := e.commit(CommitParams{SessionID: s.ID, PullRequest: true})
	if task.Status != model.TaskFailed || steps(task) != "commit:succeeded push:succeeded pull_request:failed" ||
		!strings.Contains(task.Error, "main is the repository's default branch") {
		t.Fatalf("task %+v", task)
	}
	if calls := ghCalls(t, ghLog); len(calls) != 0 {
		t.Fatalf("gh calls %q", calls)
	}
}

func TestCommitRefusals(t *testing.T) {
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{})
	if err := e.o.CheckCommit(bg, s.ID); CodeOf(err) != CodeInvalid {
		t.Fatalf("outside git: %v", err)
	}
	if _, err := e.o.CommitPreview(bg, s.ID); CodeOf(err) != CodeInvalid {
		t.Fatalf("preview outside git: %v", err)
	}

	e = newEnv(t, true)
	s = e.create(CreateSessionParams{Prompt: "slow reply please", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitFor("the turn to run", func() bool { return e.session(s.ID).Status == model.SessionRunning })
	if err := e.o.CheckCommit(bg, s.ID); CodeOf(err) != CodeConflict || !strings.Contains(err.Error(), "the agent is working") {
		t.Fatalf("while a turn runs: %v", err)
	}
	if err := e.o.Interrupt(bg, s.ID, false); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(s.ID, 1)
	if err := e.o.CheckCommit(bg, s.ID); err != nil {
		t.Fatalf("after the turn: %v", err)
	}
	if err := e.o.Archive(bg, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.o.CheckCommit(bg, s.ID); CodeOf(err) != CodeConflict {
		t.Fatalf("archived: %v", err)
	}
	if err := e.o.CheckCommit(bg, "nope"); CodeOf(err) != CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

// A prompt sent while the commit runs waits for it, as for a turn.
func TestCommitHoldsPrompts(t *testing.T) {
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	release := filepath.Join(t.TempDir(), "release")
	hook := filepath.Join(e.dir, ".git", "hooks", "pre-commit")
	os.MkdirAll(filepath.Dir(hook), 0o755)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+release+".started'\nwhile [ ! -f '"+release+"' ]; do sleep 0.02; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, s.Workspace.Path, "b.txt", "new\n")

	m := tasks.NewManager(nil)
	defer m.Shutdown()
	started := m.StartForSession(model.TaskCommit, s.ID, func(ctx context.Context, tk *tasks.Task) error {
		return e.o.Commit(ctx, CommitParams{SessionID: s.ID, Message: "Add b"}, tk)
	})
	e.waitFor("the hook to run", func() bool { _, err := os.Stat(release + ".started"); return err == nil })
	it := e.prompt(s.ID, "hello", "")
	if it.Status != model.ItemQueued {
		t.Fatalf("prompt during the commit %+v", it)
	}
	if err := e.o.CheckCommit(bg, s.ID); err != nil {
		t.Fatalf("check during the commit: %v", err)
	}
	os.WriteFile(release, nil, 0o644)
	e.waitTurn(s.ID, 1)
	task, _ := m.Get(started.ID)
	if task.Status != model.TaskSucceeded {
		t.Fatalf("task %+v", task)
	}
	// The notice comes after the commit, before or in the turn.
	if got := run(t, s.Workspace.Path, "git", "log", "-1", "--format=%s"); got != "Add b" {
		t.Fatalf("committed %q", got)
	}
}

func TestCommitMessageFallbacks(t *testing.T) {
	e := newEnv(t, true)
	e.setSystemAgent("fake")
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	// The fake agent fails on a file named fail: the files name the commit.
	write(t, s.Workspace.Path, "fail.txt", "x\n")
	write(t, s.Workspace.Path, "b.txt", "x\n")
	if p, err := e.o.CommitPreview(bg, s.ID); err != nil || p.Message != "Update 2 files" {
		t.Fatalf("preview %+v, %v", p, err)
	}
	// Without a message, the task writes one.
	task := e.commit(CommitParams{SessionID: s.ID})
	if task.Status != model.TaskSucceeded || !strings.HasSuffix(task.Steps[0].Detail, " Update 2 files") {
		t.Fatalf("task %+v", task)
	}
}

func TestParseCommitMessage(t *testing.T) {
	for reply, want := range map[string]string{
		"Fix the login redirect":                                 "Fix the login redirect",
		"```\nFix it\n\nBecause.\n```":                           "Fix it\n\nBecause.",
		"\"Fix it\"":                                             "Fix it",
		"Fix it\n\n\n\nBody line one\nline two   \n":             "Fix it\n\nBody line one\nline two",
		"`Add a thing`\n":                                        "Add a thing",
		strings.Repeat("x", 120):                                 strings.Repeat("x", 99) + "…",
		"```text\nAdd tests for the parser\n```":                 "Add tests for the parser",
		"Tidy up\n\n- one\n- two":                                "Tidy up\n\n- one\n- two",
		"  \n\nSubject after blank lines\n":                      "Subject after blank lines",
		"Subject\r\n\r\nWindows body\r\n":                        "Subject\n\nWindows body",
		"Fix “quoted” words inside":                              "Fix “quoted” words inside",
		"**Bold subject**":                                       "Bold subject",
		"Subject\n\n\"Quoted body\"":                             "Subject\n\n\"Quoted body",
		"Keep # comment lines\n\n# not a comment to git with -F": "Keep # comment lines\n\n# not a comment to git with -F",
	} {
		got, err := parseCommitMessage(reply)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", reply, got, err, want)
		}
	}
	for _, reply := range []string{"", "  \n ", "```\n```", "\"\""} {
		if got, err := parseCommitMessage(reply); err == nil {
			t.Errorf("%q: %q, want an error", reply, got)
		}
	}
}

func TestParsePullRequest(t *testing.T) {
	title, body, err := parsePullRequest("Sure:\n```json\n{\"title\": \"Fix the redirect.\", \"body\": \"It loops.\\n\\n- Stop it\\n\"}\n```")
	if err != nil || title != "Fix the redirect" || body != "It loops.\n\n- Stop it" {
		t.Fatalf("%q %q %v", title, body, err)
	}
	for _, reply := range []string{"no json", `{"title": ""}`, `{"title": 3}`} {
		if _, _, err := parsePullRequest(reply); err == nil {
			t.Errorf("%q: no error", reply)
		}
	}
}

func TestPlainPullRequest(t *testing.T) {
	one := []gitx.Commit{{Subject: "Fix it", Body: "Because."}}
	if title, body := plainPullRequest("Whatever", "ra/x", one); title != "Fix it" || body != "Because." {
		t.Fatalf("one commit: %q %q", title, body)
	}
	two := []gitx.Commit{{Subject: "Second"}, {Subject: "First"}}
	if title, body := plainPullRequest("Fix the login redirect", "ra/x", two); title != "Fix the login redirect" || body != "- First\n- Second" {
		t.Fatalf("two commits: %q %q", title, body)
	}
	if title, _ := plainPullRequest("New session", "ra/x", two); title != "First" {
		t.Fatalf("untitled session: %q", title)
	}
	if title, _ := plainPullRequest("", "ra/x", nil); title != "ra/x" {
		t.Fatalf("no commits: %q", title)
	}
}

// What a command prints to stdout and stderr shows in the order it wrote
// it, as gh's "Creating pull request…" before the address. Without a task
// to show it in, it returns its stdout alone, for gh's JSON.
func TestRunCommandKeepsTheOrder(t *testing.T) {
	script := `i=0; while [ $i -lt 200 ]; do echo "out $i"; echo "err $i" >&2; i=$((i+1)); done`
	var shown strings.Builder
	out, err := runCommand(bg, t.TempDir(), "", &shown, "/bin/sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	var want, wantOut strings.Builder
	for i := range 200 {
		fmt.Fprintf(&want, "out %d\nerr %d\n", i, i)
		fmt.Fprintf(&wantOut, "out %d\n", i)
	}
	if shown.String() != want.String() {
		t.Fatalf("shown out of order:\n%s", shown.String())
	}
	if out != want.String() {
		t.Fatalf("returned %q", out)
	}

	out, err = runCommand(bg, t.TempDir(), "", nil, "/bin/sh", "-c", script)
	if err != nil || out != wantOut.String() {
		t.Fatalf("without a task: %q, %v", out, err)
	}

	// An error says the last line it printed, from either.
	shown.Reset()
	_, err = runCommand(bg, t.TempDir(), "", &shown, "/bin/sh", "-c", `echo "To origin" >&2; echo "! [rejected] main -> main (fetch first)" >&2; echo done; exit 1`)
	if err == nil || err.Error() != "sh -c failed: done" || shown.String() != "To origin\n! [rejected] main -> main (fetch first)\ndone\n" {
		t.Fatalf("error %v, shown %q", err, shown.String())
	}
	_, err = runCommand(bg, t.TempDir(), "", nil, "/bin/sh", "-c", `echo "! [rejected]" >&2; echo done; exit 1`)
	if err == nil || err.Error() != "sh -c failed: ! [rejected]" {
		t.Fatalf("error without a task %v", err)
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine("/opt/bin/gh", []string{"pr", "create", "--title", "Fix it's thing", "--body-file", "-", "--base", "main", ""})
	if want := `gh pr create --title 'Fix it'\''s thing' --body-file - --base main ''`; got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}
