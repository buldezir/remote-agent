package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// The pull request tests never reach GitHub: origin's address is GitHub's,
// but nothing fetches or pushes, and gh is a script first on PATH that
// answers pr list with $FAKE_GH_PRS.

const trackerGH = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
if [ -n "$FAKE_GH_SIGNED_OUT" ]; then
  echo "To get started with GitHub CLI, please run:  gh auth login" >&2
  exit 4
fi
case "$1 $2" in
"pr list") printf '%s\n' "${FAKE_GH_PRS:-[]}" ;;
*) echo "fake gh: unknown command $*" >&2; exit 1 ;;
esac
`

func withTrackerGH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(trackerGH), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("FAKE_GH_PRS", "")
	t.Setenv("FAKE_GH_SIGNED_OUT", "")
	return log
}

// githubOrigin makes origin a GitHub address whose main the project has
// fetched, without a network: the remote-tracking refs are set by hand.
func (e *env) githubOrigin() {
	e.t.Helper()
	run(e.t, e.dir, "git", "remote", "add", "origin", "git@github.com:test/repo.git")
	run(e.t, e.dir, "git", "update-ref", "refs/remotes/origin/main", "HEAD")
	run(e.t, e.dir, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

// lookNow runs a round of looks at the session, as if prSoon had passed.
func (e *env) lookNow(ids ...string) {
	e.o.prs.mu.Lock()
	for _, id := range ids {
		e.o.prs.checked[id] = time.Time{}
	}
	e.o.prs.mu.Unlock()
	for _, id := range ids {
		e.o.nudgePullRequest(id)
	}
	e.o.RefreshPullRequests(bg)
}

func TestPullRequestTracking(t *testing.T) {
	ghLog := withTrackerGH(t)
	e := newEnv(t, true)
	e.githubOrigin()
	root := e.create(CreateSessionParams{})
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/feature"}})
	dir := s.Workspace.Path

	// Unpushed, and on the default branch: nothing to ask GitHub.
	e.lookNow(root.ID, s.ID)
	if calls := ghCalls(t, ghLog); calls != nil {
		t.Fatalf("gh calls %q", calls)
	}

	// Pushed, with a pull request open as a draft.
	run(t, dir, "git", "update-ref", "refs/remotes/origin/ra/feature", "HEAD")
	t.Setenv("FAKE_GH_PRS", `[{"number":12,"title":"Feature","url":"https://github.com/test/repo/pull/12","state":"OPEN","isDraft":true,
		"headRefName":"ra/feature","baseRefName":"main","statusCheckRollup":[{"status":"IN_PROGRESS"}]}]`)
	before := e.session(s.ID).UpdatedAt
	e.lookNow(root.ID, s.ID)
	got := e.session(s.ID)
	want := model.PullRequest{Forge: "github", Number: 12, Title: "Feature", URL: "https://github.com/test/repo/pull/12",
		State: model.PullRequestDraft, Head: "ra/feature", Base: "main", Checks: model.ChecksPending}
	if got.PullRequest == nil || *got.PullRequest != want {
		t.Fatalf("pull request %+v", got.PullRequest)
	}
	if !got.UpdatedAt.Equal(before) {
		t.Fatal("finding the pull request moved the session in the lists")
	}
	if calls := ghCalls(t, ghLog); len(calls) != 1 || calls[0] != "pr list --head ra/feature --state all --limit 10 --json number,title,url,state,isDraft,headRefName,baseRefName,reviewDecision,statusCheckRollup" {
		t.Fatalf("gh calls %q", calls)
	}
	if e.session(root.ID).PullRequest != nil {
		t.Fatal("the session on main got one")
	}

	// Not nudged, and no client connected: nothing's due. Nudged right after
	// a look: it waits for prSoon.
	e.o.RefreshPullRequests(bg)
	e.o.nudgePullRequest(s.ID)
	e.o.RefreshPullRequests(bg)
	if calls := ghCalls(t, ghLog); len(calls) != 1 {
		t.Fatalf("gh calls %q", calls)
	}

	// With a client connected, an open one is due after prRefresh.
	done := e.o.ClientConnected()
	defer done()
	e.o.prs.mu.Lock()
	delete(e.o.prs.nudged, s.ID)
	e.o.prs.checked[s.ID] = time.Now().Add(-prRefresh)
	e.o.prs.checked[root.ID] = time.Now()
	e.o.prs.mu.Unlock()
	t.Setenv("FAKE_GH_PRS", `[{"number":12,"title":"Feature","url":"https://github.com/test/repo/pull/12","state":"MERGED",
		"headRefName":"ra/feature","baseRefName":"main","reviewDecision":"APPROVED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}]`)
	e.o.RefreshPullRequests(bg)
	if pr := e.session(s.ID).PullRequest; pr == nil || pr.State != model.PullRequestMerged || pr.Checks != model.ChecksPassing || pr.Review != model.ReviewApproved {
		t.Fatalf("merged %+v", pr)
	}
	// A merged one waits for prSettled.
	e.o.prs.mu.Lock()
	e.o.prs.checked[s.ID] = time.Now().Add(-prRefresh)
	e.o.prs.mu.Unlock()
	e.o.RefreshPullRequests(bg)
	if calls := ghCalls(t, ghLog); len(calls) != 2 {
		t.Fatalf("gh calls %q", calls)
	}

	// gh signed out: rad keeps what it had, and leaves gh alone for prRetry.
	t.Setenv("FAKE_GH_SIGNED_OUT", "1")
	e.lookNow(s.ID)
	e.lookNow(s.ID)
	if calls := ghCalls(t, ghLog); len(calls) != 3 {
		t.Fatalf("gh calls %q", calls)
	}
	if pr := e.session(s.ID).PullRequest; pr == nil || pr.Number != 12 {
		t.Fatalf("lost the pull request: %+v", pr)
	}

	// Another branch: the pull request was the old one's.
	run(t, dir, "git", "checkout", "-q", "-b", "ra/next")
	a, _ := e.o.actor(bg, s.ID)
	a.syncBranch(bg)
	if got := e.session(s.ID); got.Workspace.Branch != "ra/next" || got.PullRequest != nil {
		t.Fatalf("after the switch: %q, %+v", got.Workspace.Branch, got.PullRequest)
	}
	if !e.o.prs.nudged[s.ID] {
		t.Fatal("the switch didn't nudge a look")
	}
}

// A commit that opens a pull request shows it on the session at once.
func TestCommitShowsPullRequest(t *testing.T) {
	withFakeGH(t)
	e := newEnv(t, true)
	e.addOrigin()
	s := e.create(CreateSessionParams{Title: "Edit", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/feature"}})
	write(t, s.Workspace.Path, "b.txt", "new\n")
	if task := e.commit(CommitParams{SessionID: s.ID, Message: "Add b", PullRequest: true}); task.Status != model.TaskSucceeded {
		t.Fatalf("task %+v", task)
	}
	pr := e.session(s.ID).PullRequest
	if pr == nil || pr.Forge != "github" || pr.Number != 12 || pr.URL != "https://github.com/test/repo/pull/12" || pr.State != model.PullRequestOpen ||
		pr.Head != "ra/feature" || pr.Base != "main" || pr.Title != "Add b" {
		t.Fatalf("pull request %+v", pr)
	}
}
