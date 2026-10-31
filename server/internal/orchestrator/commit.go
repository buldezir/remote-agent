package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// Committing a session's work from the apps. git.commitPreview shows what a
// commit would take, with a message the system agent writes from the diff,
// and git.commit, a server task, commits it in the folder the agent works
// in, pushes the branch and opens a pull request with GitHub's gh, as far
// as the user asked, reporting each step as it goes.

const (
	// How much of a diff the system agent reads, for a commit message and
	// for a pull request.
	maxMessageDiff = 24 << 10
	maxRequestDiff = 16 << 10
	// writeTimeout is the system agent's time to write either; the user waits.
	writeTimeout = 60 * time.Second
	// Each step's time; a commit runs the repository's hooks.
	commitTimeout = 10 * time.Minute
	pushTimeout   = 5 * time.Minute
	ghTimeout     = 2 * time.Minute
)

// The steps of a commit task.
const (
	stepCommit      = "commit"
	stepPush        = "push"
	stepPullRequest = "pull_request"
)

// CommitPreview is what git.commit would commit in a session, and a message
// for it.
type CommitPreview struct {
	Branch string          `json:"branch,omitempty"` // checked out where the agent works; absent: detached
	Files  []gitx.FileStat `json:"files"`            // everything uncommitted, untracked files included
	// Message is the system agent's for the files, or rad's own when there
	// is none or it fails; "" with no files.
	Message string `json:"message"`
	Remote  string `json:"remote,omitempty"` // where a push goes; absent: nowhere
	Base    string `json:"base,omitempty"`   // the branch a pull request goes into; absent: the repository's default
	// Ahead counts the branch's commits that its remote branch doesn't
	// have, or, before its first push, that base doesn't.
	Ahead int `json:"ahead"`
	// NoPullRequest says why a pull request can't be opened from the
	// branch, such as the repository's default branch; absent: it can, or
	// there's no push.
	NoPullRequest string `json:"noPullRequest,omitempty"`
}

// CommitParams are git.commit's.
type CommitParams struct {
	SessionID string `json:"sessionId"`
	// Message is the commit's; empty: the system agent writes one, as for
	// the preview.
	Message     string `json:"message,omitempty"`
	Push        bool   `json:"push,omitempty"`
	PullRequest bool   `json:"pullRequest,omitempty"` // pushes too
}

// Progress is where a commit reports how it goes: its server task
// (tasks.Task).
type Progress interface {
	io.Writer
	Println(format string, args ...any)
	Step(model.TaskStep)
	SetURL(url string)
}

// CheckCommit refuses a commit the session can't make now, before its task
// starts.
func (o *Orchestrator) CheckCommit(ctx context.Context, sessionID string) error {
	_, err := o.commitActor(ctx, sessionID)
	return err
}

// commitActor is the session's actor, for a commit: in a git repository,
// not archived, and with no turn running.
func (o *Orchestrator) commitActor(ctx context.Context, sessionID string) (*actor, error) {
	a, err := o.gitActor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a, a.commitBlocked()
}

// commitBlocked says why the session can't commit now. The caller holds
// a.mu.
func (a *actor) commitBlocked() error {
	switch {
	case a.deleted:
		return errorf(CodeNotFound, "session %s not found", a.sess.ID)
	case a.sess.Archived || a.archiving:
		return errorf(CodeConflict, "the session is archived")
	case a.turn != nil:
		return errorf(CodeConflict, "the agent is working: commit once its turn ends")
	}
	if fi, err := os.Stat(a.dir()); err != nil || !fi.IsDir() {
		return errorf(CodeConflict, "the session's folder is gone")
	}
	return nil
}

// holdForCommit keeps prompts waiting in the queue while a commit runs, as
// they wait for a turn, unless a turn runs: then it refuses. release lets
// the next one start.
func (a *actor) holdForCommit(ctx context.Context) (release func(), err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.commitBlocked(); err != nil {
		return nil, err
	}
	a.holds++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.holds--
			a.startNext(context.WithoutCancel(ctx))
		})
	}, nil
}

// commitPlace is where a session commits, and what it knows of it.
type commitPlace struct {
	dir         string // the folder the agent works in
	ws          model.Workspace
	projectPath string
	projectName string
	title       string // the session's
}

func (a *actor) commitPlace() commitPlace {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := commitPlace{dir: a.dir(), ws: a.sess.Workspace, title: a.sess.Title}
	if a.project != nil {
		c.projectPath, c.projectName = a.project.Path, a.project.Name
	}
	return c
}

// CommitPreview shows what git.commit would commit in the session, with a
// message for it.
func (o *Orchestrator) CommitPreview(ctx context.Context, sessionID string) (*CommitPreview, error) {
	a, err := o.commitActor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	c := a.commitPlace()
	files, patch, truncated, err := gitx.Uncommitted(ctx, c.dir, maxMessageDiff)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []gitx.FileStat{}
	}
	p := &CommitPreview{Branch: gitx.CurrentBranch(ctx, c.dir), Files: files}
	p.Remote, _ = pushRemote(ctx, c.dir, p.Branch)
	p.Base = c.baseBranch(ctx, p.Branch, p.Remote)
	p.NoPullRequest = c.noPullRequest(ctx, p.Branch, p.Remote, p.Base)
	switch {
	case p.Remote != "" && gitx.RemoteBranchExists(ctx, c.dir, p.Remote, p.Branch):
		p.Ahead = gitx.CountCommits(ctx, c.dir, p.Remote+"/"+p.Branch+"..HEAD")
	case p.Base != "":
		p.Ahead = gitx.CountCommits(ctx, c.dir, c.baseRev(ctx, p.Base, p.Remote)+"..HEAD")
	default:
		p.Ahead = gitx.CountCommits(ctx, c.dir, "HEAD")
	}
	if len(files) > 0 {
		p.Message = o.commitMessage(ctx, c, files, patch, truncated)
	}
	return p, nil
}

// pushRemote is where branch goes: the remote it tracks a branch of its own
// name on (tracked), else origin; "" for neither, or a detached HEAD.
func pushRemote(ctx context.Context, dir, branch string) (remote string, tracked bool) {
	if branch == "" {
		return "", false
	}
	remotes := gitx.Remotes(ctx, dir)
	// A worktree's branch made from origin/main tracks main: not its own.
	if r, merge := gitx.Upstream(ctx, dir, branch); merge == branch && slices.Contains(remotes, r) {
		return r, true
	}
	if slices.Contains(remotes, "origin") {
		return "origin", false
	}
	return "", false
}

// baseBranch is the branch a pull request from branch goes into: the one
// the session's worktree was made from, else the remote's default branch,
// else the project's own (for a worktree), else main or master; "" when
// none of them is there.
func (c commitPlace) baseBranch(ctx context.Context, branch, remote string) string {
	var candidates []string
	if b := c.ws.BaseRef; b != "" {
		b = strings.TrimPrefix(strings.TrimPrefix(b, "refs/heads/"), "refs/remotes/")
		if remote != "" {
			b = strings.TrimPrefix(b, remote+"/")
		}
		candidates = append(candidates, b)
	}
	if remote != "" {
		candidates = append(candidates, gitx.RemoteDefaultBranch(ctx, c.dir, remote))
	}
	if c.ws.Kind == model.WorkspaceWorktree && c.projectPath != "" {
		candidates = append(candidates, gitx.CurrentBranch(ctx, c.projectPath))
	}
	candidates = append(candidates, "main", "master")
	for _, b := range candidates {
		if b == "" || b == "HEAD" || b == branch {
			continue
		}
		if gitx.BranchExists(ctx, c.dir, b) || remote != "" && gitx.RemoteBranchExists(ctx, c.dir, remote, b) {
			return b
		}
	}
	return ""
}

// noPullRequest says why a pull request can't go from branch to remote,
// into base, or "". It can't from the repository's default branch, which
// has nowhere to go: the branch the remote's HEAD names, as the last fetch
// saw it, else main or master when no other branch is there to go into.
func (c commitPlace) noPullRequest(ctx context.Context, branch, remote, base string) string {
	if branch == "" || remote == "" {
		return "" // the push fails first, and says why
	}
	def := gitx.RemoteDefaultBranch(ctx, c.dir, remote)
	if def == "" && base == "" && (branch == "main" || branch == "master") {
		def = branch
	}
	if branch != def {
		return ""
	}
	return branch + " is the repository's default branch: a pull request needs a branch of its own"
}

// baseRev names base as a commit: the remote's, which the pull request goes
// into, where the last fetch saw it.
func (c commitPlace) baseRev(ctx context.Context, base, remote string) string {
	if remote != "" && gitx.RemoteBranchExists(ctx, c.dir, remote, base) {
		return remote + "/" + base
	}
	return base
}

// Commit commits the session's changes, then pushes its branch and opens a
// pull request, as far as p asks, reporting to pr. It runs as a task.
func (o *Orchestrator) Commit(ctx context.Context, p CommitParams, pr Progress) error {
	a, err := o.commitActor(ctx, p.SessionID)
	if err != nil {
		return err
	}
	r := &commitRun{o: o, a: a, p: pr, place: a.commitPlace()}
	r.branch = gitx.CurrentBranch(ctx, r.place.dir)
	r.remote, r.tracked = pushRemote(ctx, r.place.dir, r.branch)
	push := p.Push || p.PullRequest
	r.add(stepCommit, "Commit")
	if push {
		// ra says where a commit it follows goes from this title.
		title := "Push"
		if r.remote != "" {
			title = "Push to " + r.remote
		}
		r.add(stepPush, title)
	}
	if p.PullRequest {
		r.add(stepPullRequest, "Open a pull request")
	}
	defer r.record(ctx)
	if err := r.commit(ctx, p.Message); err != nil {
		return err
	}
	if push {
		if err := r.push(ctx); err != nil {
			return err
		}
	}
	if p.PullRequest {
		return r.pullRequest(ctx)
	}
	return nil
}

type commitRun struct {
	o     *Orchestrator
	a     *actor
	p     Progress
	place commitPlace
	steps []model.TaskStep

	branch  string
	remote  string
	tracked bool     // the branch tracks its namesake on remote
	done    []string // what it did, for the transcript
}

func (r *commitRun) add(id, title string) {
	st := model.TaskStep{ID: id, Title: title, Status: model.StepPending}
	r.steps = append(r.steps, st)
	r.p.Step(st)
}

func (r *commitRun) set(id string, status model.TaskStepStatus, detail string) {
	i := slices.IndexFunc(r.steps, func(s model.TaskStep) bool { return s.ID == id })
	r.steps[i].Status, r.steps[i].Detail = status, detail
	r.p.Step(r.steps[i])
}

func (r *commitRun) fail(id string, err error) error {
	r.set(id, model.StepFailed, err.Error())
	return err
}

// exec runs a command in the session's folder, showing it and what it
// prints in the task, with stdin as its input. It returns what it printed,
// stderr too.
func (r *commitRun) exec(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r.p.Println("$ %s", commandLine(name, args))
	return runCommand(ctx, r.place.dir, stdin, r.p, name, args...)
}

func (r *commitRun) commit(ctx context.Context, message string) error {
	ctx, cancel := context.WithTimeout(ctx, commitTimeout)
	defer cancel()
	dir := r.place.dir
	r.set(stepCommit, model.StepRunning, "")
	if !gitx.Dirty(ctx, dir) {
		r.set(stepCommit, model.StepSkipped, "Nothing to commit")
		return nil
	}
	msg := cleanMessage(message)
	if msg == "" {
		r.set(stepCommit, model.StepRunning, "Writing the message…")
		files, patch, truncated, err := gitx.Uncommitted(ctx, dir, maxMessageDiff)
		if err != nil {
			return r.fail(stepCommit, err)
		}
		msg = r.o.commitMessage(ctx, r.place, files, patch, truncated)
	}
	r.set(stepCommit, model.StepRunning, "Committing…")
	release, err := r.a.holdForCommit(ctx)
	if err != nil {
		return r.fail(stepCommit, err)
	}
	defer release()
	if _, err := r.exec(ctx, "", "git", "add", "-A"); err != nil {
		return r.fail(stepCommit, err)
	}
	if _, err := r.exec(ctx, msg, "git", "commit", "-F", "-"); err != nil {
		return r.fail(stepCommit, err)
	}
	release()
	head, err := gitx.Log(ctx, dir, "HEAD", 1)
	if err != nil || len(head) == 0 {
		r.set(stepCommit, model.StepSucceeded, "")
		r.done = append(r.done, "committed the changes")
		return nil
	}
	sha := head[0].SHA[:min(7, len(head[0].SHA))]
	r.set(stepCommit, model.StepSucceeded, sha+" "+head[0].Subject)
	r.done = append(r.done, fmt.Sprintf("committed %s “%s”", sha, head[0].Subject))
	return nil
}

func (r *commitRun) push(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	r.set(stepPush, model.StepRunning, "")
	if r.branch == "" {
		return r.fail(stepPush, errors.New("HEAD is detached: check out a branch to push it"))
	}
	if r.remote == "" {
		return r.fail(stepPush, noRemote(ctx, r.place.dir))
	}
	r.set(stepPush, model.StepRunning, r.branch+" → "+r.remote)
	args := []string{"push"}
	if !r.tracked {
		args = append(args, "--set-upstream")
	}
	if _, err := r.exec(ctx, "", "git", append(args, r.remote, r.branch)...); err != nil {
		return r.fail(stepPush, err)
	}
	r.set(stepPush, model.StepSucceeded, r.branch+" → "+r.remote)
	r.done = append(r.done, fmt.Sprintf("pushed %s to %s", r.branch, r.remote))
	return nil
}

// noRemote says why a branch has nowhere to go.
func noRemote(ctx context.Context, dir string) error {
	if remotes := gitx.Remotes(ctx, dir); len(remotes) > 0 {
		return fmt.Errorf("the branch has no upstream, and the repository has no remote named origin (it has %s)", strings.Join(remotes, ", "))
	}
	return errors.New("the repository has no remote to push to: add one on the computer, such as with git remote add origin <url>")
}

func (r *commitRun) pullRequest(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout+writeTimeout)
	defer cancel()
	r.set(stepPullRequest, model.StepRunning, "")
	// As the preview said.
	base := r.place.baseBranch(ctx, r.branch, r.remote)
	if why := r.place.noPullRequest(ctx, r.branch, r.remote, base); why != "" {
		return r.fail(stepPullRequest, errors.New(why))
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return r.fail(stepPullRequest, errors.New("GitHub's command line tool, gh, isn't installed on the computer: install it (https://cli.github.com), then sign in with gh auth login"))
	}
	dir := r.place.dir

	// One may be open already, which the push brought up to date.
	out, err := runCommand(ctx, dir, "", nil, gh, "pr", "list", "--head", r.branch, "--state", "open", "--json", "number,url,title", "--limit", "1")
	if err != nil {
		return r.fail(stepPullRequest, ghError(err))
	}
	var open []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &open); err != nil {
		return r.fail(stepPullRequest, fmt.Errorf("gh pr list: %w", err))
	}
	if len(open) > 0 {
		r.foundOpen(open[0].Number, open[0].URL, open[0].Title)
		return nil
	}

	r.set(stepPullRequest, model.StepRunning, "Writing the title and description…")
	title, body := r.o.pullRequestText(ctx, r.place, r.branch, base, r.remote)
	r.set(stepPullRequest, model.StepRunning, "Opening…")
	args := []string{"pr", "create", "--head", r.branch, "--title", title, "--body-file", "-"}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err = r.exec(ctx, body, gh, args...)
	if err != nil {
		// Opened meanwhile, from elsewhere.
		var ce *commandError
		if errors.As(err, &ce) && strings.Contains(ce.output, "already exists") {
			if url := lastURL(ce.output); url != "" {
				r.foundOpen(prNumber(url), url, "")
				return nil
			}
		}
		return r.fail(stepPullRequest, ghError(err))
	}
	url := lastURL(out)
	if url == "" {
		return r.fail(stepPullRequest, errors.New("gh pr create didn't say where the pull request is"))
	}
	r.p.SetURL(url)
	r.set(stepPullRequest, model.StepSucceeded, prLabel(prNumber(url), title))
	r.done = append(r.done, fmt.Sprintf("opened pull request %s", url))
	return nil
}

// foundOpen reports the pull request that was open for the branch already.
func (r *commitRun) foundOpen(number int, url, title string) {
	r.p.Println("A pull request is open for %s already: %s", r.branch, url)
	r.p.SetURL(url)
	r.set(stepPullRequest, model.StepSucceeded, "Already open: "+prLabel(number, title))
	r.done = append(r.done, fmt.Sprintf("found pull request %s open", url))
}

func prLabel(number int, title string) string {
	s := ""
	if number > 0 {
		s = "#" + strconv.Itoa(number)
	}
	return strings.TrimSpace(s + " " + title)
}

var prURL = regexp.MustCompile(`/pull/(\d+)`)

func prNumber(url string) int {
	m := prURL.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

var webAddress = regexp.MustCompile(`https?://\S+`)

func lastURL(s string) string {
	all := webAddress.FindAllString(s, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// ghError says what went wrong with gh: exit status 4 is its "sign in".
func ghError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 4 {
		return errors.New("gh isn't signed in to GitHub: sign in on the computer with gh auth login")
	}
	return err
}

// record tells the transcript, and the agent with the next prompt, what the
// commit did.
func (r *commitRun) record(ctx context.Context) {
	if len(r.done) == 0 {
		return
	}
	what := r.done[0]
	if n := len(r.done); n > 1 {
		what = strings.Join(r.done[:n-1], ", ") + " and " + r.done[n-1]
	}
	a := r.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted {
		return
	}
	notice := a.newItem(model.ItemNotice, model.ItemCompleted)
	notice.Text = strings.ToUpper(what[:1]) + what[1:]
	if !webAddress.MatchString(r.done[len(r.done)-1]) {
		notice.Text += "." // not after an address, where it would read as part of it
	}
	note := "[Note from the user's tooling: the user " + what + ".]"
	a.note = strings.TrimSpace(a.note + "\n" + note)
	a.write(context.WithoutCancel(ctx), func(w *store.W) error { return a.putItem(w, notice) })
}

// Running git and gh.

// commandEnv keeps git and gh from asking: nobody is at the computer to
// answer.
var commandEnv = []string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "NO_COLOR=1"}

type commandError struct {
	what   string // such as "git push"
	err    error
	output string // its stderr, or all it printed where that was shown
}

func (e *commandError) Error() string {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(e.output), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l // the last one says most
		}
	}
	if line == "" {
		return fmt.Sprintf("%s failed: %v", e.what, e.err)
	}
	return fmt.Sprintf("%s failed: %s", e.what, line)
}

func (e *commandError) Unwrap() error { return e.err }

// runCommand runs a command in dir, without a terminal, so that nothing it
// starts (ssh, a credential helper) can ask, and with stdin as its input.
// It returns its stdout. What it prints also goes to shown, unless that's
// nil, through one pipe for stdout and stderr, which keeps the order it
// wrote in: two are copied apart, and gh's "Creating pull request…" could
// show after the address it prints. It then returns both, as shown.
func runCommand(ctx context.Context, dir, stdin string, shown io.Writer, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), commandEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if shown != nil {
		// The same writer for both, which exec gives one pipe.
		w := io.MultiWriter(&stdout, shown)
		cmd.Stdout, cmd.Stderr = w, w
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.String(), ctx.Err()
		}
		what := filepath.Base(name)
		if len(args) > 0 {
			what += " " + args[0]
			if what == "gh pr" && len(args) > 1 {
				what += " " + args[1]
			}
		}
		output := stderr.String()
		if shown != nil {
			output = stdout.String()
		}
		return stdout.String(), &commandError{what: what, err: err, output: output}
	}
	return stdout.String(), nil
}

// commandLine shows a command as a shell would take it.
func commandLine(name string, args []string) string {
	parts := []string{filepath.Base(name)}
	for _, a := range args {
		if a == "" || strings.ContainsFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@+,%", r))
		}) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// The system agent's messages.

var errNoSystemAgent = errors.New("no system agent")

// askSystemAgent runs the system agent once on prompt, if one is set.
func (o *Orchestrator) askSystemAgent(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	settings, err := o.st.Settings(ctx)
	if err != nil {
		return "", err
	}
	if settings.SystemAgent == nil {
		return "", errNoSystemAgent
	}
	return o.oneShot(ctx, *settings.SystemAgent, prompt)
}

// commitMessage has the system agent write a commit message for the
// changes, or makes a plain one when there's none or it fails.
func (o *Orchestrator) commitMessage(ctx context.Context, c commitPlace, files []gitx.FileStat, patch string, truncated bool) string {
	var recent []string
	if log, err := gitx.Log(ctx, c.dir, "HEAD", 8); err == nil {
		for _, cm := range log {
			recent = append(recent, cm.Subject)
		}
	}
	start := time.Now()
	reply, err := o.askSystemAgent(ctx, commitMessagePrompt(c.projectName, c.title, recent, files, patch, truncated))
	if err == nil {
		var msg string
		if msg, err = parseCommitMessage(reply); err == nil {
			o.log.Info("commit message written", "took", time.Since(start).Round(time.Millisecond))
			return msg
		}
	}
	if !errors.Is(err, errNoSystemAgent) {
		o.log.Warn("writing the commit message failed", "err", err)
	}
	return plainCommitMessage(c.title, files)
}

func commitMessagePrompt(projectName, title string, recent []string, files []gitx.FileStat, patch string, truncated bool) string {
	var b strings.Builder
	b.WriteString("You write git commit messages. Don't change anything, and don't use tools. Reply with only the commit message, " +
		"without quotes or a code block: a subject line in the imperative mood, at most 72 characters, without a full stop; " +
		"then, if the change needs explaining, a blank line and a short body wrapped at 72 characters.\n\n")
	if projectName != "" {
		fmt.Fprintf(&b, "The project is %q. ", projectName)
	}
	if title != "" && title != "New session" {
		fmt.Fprintf(&b, "The work was asked for in a session titled %q.", title)
	}
	b.WriteString("\n")
	if len(recent) > 0 {
		b.WriteString("\nThe latest commits' subjects, for their style:\n")
		for _, s := range recent {
			b.WriteString("- " + s + "\n")
		}
	}
	b.WriteString("\nThe changes:\n<diff>\n")
	writeFiles(&b, files)
	b.WriteString("\n" + patch)
	if truncated {
		b.WriteString("[…the rest of the diff is left out]\n")
	}
	b.WriteString("</diff>\n")
	return b.String()
}

func writeFiles(b *strings.Builder, files []gitx.FileStat) {
	for _, f := range files {
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath + " → " + f.Path
		}
		fmt.Fprintf(b, "%s %s (+%d −%d)\n", f.Status, name, f.Additions, f.Deletions)
	}
}

// parseCommitMessage takes the message out of the agent's reply, which may
// come in a code block or quotes.
func parseCommitMessage(reply string) (string, error) {
	s := strings.TrimSpace(reply)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		if _, after, ok := strings.Cut(s, "\n"); ok {
			s = after
		} else {
			s = ""
		}
	}
	msg := cleanMessage(s)
	if msg == "" {
		return "", errors.New("the reply has no message")
	}
	subject, body, _ := strings.Cut(msg, "\n")
	subject = strings.TrimSpace(strings.Trim(subject, "\"'`“”‘’*"))
	if subject == "" {
		return "", errors.New("the reply has no subject")
	}
	if utf8.RuneCountInString(subject) > 100 {
		subject = string([]rune(subject)[:99]) + "…"
	}
	if body = strings.TrimSpace(strings.TrimRight(body, "\"'`”’")); body != "" {
		return subject + "\n\n" + body, nil
	}
	return subject, nil
}

// cleanMessage trims a message, and its lines' ends, and keeps at most one
// blank line in a row.
func cleanMessage(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// plainCommitMessage is rad's own: the session's title, or the files.
func plainCommitMessage(title string, files []gitx.FileStat) string {
	if t := cleanTitle(title); t != "" && t != "New session" {
		return t
	}
	switch len(files) {
	case 0:
		return "Update"
	case 1:
		return "Update " + files[0].Path
	}
	return fmt.Sprintf("Update %d files", len(files))
}

// pullRequestText has the system agent write a pull request's title and
// description from the branch's commits, or makes plain ones.
func (o *Orchestrator) pullRequestText(ctx context.Context, c commitPlace, branch, base, remote string) (title, body string) {
	rangeSpec, from := "HEAD", ""
	if base != "" {
		rev := c.baseRev(ctx, base, remote)
		rangeSpec = rev + "..HEAD"
		from, _ = gitx.MergeBase(ctx, c.dir, rev, "HEAD")
	}
	commits, _ := gitx.Log(ctx, c.dir, rangeSpec, 50)
	var files []gitx.FileStat
	var patch string
	var truncated bool
	if from != "" {
		files, _ = gitx.DiffFiles(ctx, c.dir, from, "HEAD")
		patch, truncated, _ = gitx.Patch(ctx, c.dir, from, "HEAD", nil, maxRequestDiff)
	}
	start := time.Now()
	reply, err := o.askSystemAgent(ctx, pullRequestPrompt(c.projectName, branch, base, commits, files, patch, truncated))
	if err == nil {
		if title, body, err = parsePullRequest(reply); err == nil {
			o.log.Info("pull request written", "took", time.Since(start).Round(time.Millisecond))
			return title, body
		}
	}
	if !errors.Is(err, errNoSystemAgent) {
		o.log.Warn("writing the pull request failed", "err", err)
	}
	return plainPullRequest(c.title, branch, commits)
}

func pullRequestPrompt(projectName, branch, base string, commits []gitx.Commit, files []gitx.FileStat, patch string, truncated bool) string {
	var b strings.Builder
	b.WriteString("You write GitHub pull requests. Don't change anything, and don't use tools. Reply with only a JSON object:\n")
	b.WriteString(`{"title": "<what the branch does, in at most 72 characters, sentence case, without a full stop>", ` +
		`"body": "<Markdown: a sentence or two on what changed and why, then the main changes as a short list>"}` + "\n\n")
	if projectName != "" {
		fmt.Fprintf(&b, "The project is %q. ", projectName)
	}
	if base != "" {
		fmt.Fprintf(&b, "The branch %q goes into %q.", branch, base)
	} else {
		fmt.Fprintf(&b, "The branch is %q.", branch)
	}
	b.WriteString(" Its commits, newest first:\n<commits>\n")
	for _, c := range commits {
		b.WriteString("- " + c.Subject + "\n")
		if c.Body != "" {
			b.WriteString("  " + strings.ReplaceAll(c.Body, "\n", "\n  ") + "\n")
		}
	}
	b.WriteString("</commits>\n")
	if len(files) > 0 || patch != "" {
		b.WriteString("Its changes:\n<diff>\n")
		writeFiles(&b, files)
		b.WriteString("\n" + patch)
		if truncated {
			b.WriteString("[…the rest of the diff is left out]\n")
		}
		b.WriteString("</diff>\n")
	}
	return b.String()
}

// parsePullRequest reads the JSON object in the agent's reply, which may
// come with words around it or in a code block.
func parsePullRequest(reply string) (title, body string, err error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return "", "", errors.New("the reply has no JSON object")
	}
	var v struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &v); err != nil {
		return "", "", fmt.Errorf("the reply's JSON: %w", err)
	}
	if title = cleanTitle(v.Title); title == "" {
		return "", "", errors.New("the reply has no title")
	}
	return title, strings.TrimSpace(v.Body), nil
}

// plainPullRequest is rad's own: a lone commit's subject and body, or the
// session's title and the commits' subjects.
func plainPullRequest(sessionTitle, branch string, commits []gitx.Commit) (title, body string) {
	if len(commits) == 1 {
		return cleanTitle(commits[0].Subject), commits[0].Body
	}
	title = cleanTitle(sessionTitle)
	if title == "" || title == "New session" {
		title = branch
		if len(commits) > 0 {
			title = cleanTitle(commits[len(commits)-1].Subject)
		}
	}
	var b strings.Builder
	for i := len(commits) - 1; i >= 0; i-- {
		b.WriteString("- " + commits[i].Subject + "\n")
	}
	return title, strings.TrimSpace(b.String())
}
