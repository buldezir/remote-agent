package fake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"remote-agent/internal/harness"
)

// OneShot names a session from the request in the prompt, between <request>
// and </request>: "Named: " and its first words, or a failure when it says
// "fail". "slow" takes two seconds. Asked for a commit message, it answers
// "Change " and the files in the <diff>; for a pull request, its title is
// "Fake: " and the latest of the <commits>' subjects.
func (Harness) OneShot(ctx context.Context, o harness.OneShotOptions) (string, error) {
	switch {
	case strings.HasPrefix(o.Prompt, "You write GitHub pull requests"):
		commits, _ := between(o.Prompt, "<commits>", "</commits>")
		return pullRequest(commits)
	case strings.HasPrefix(o.Prompt, "You write git commit messages"):
		diff, _ := between(o.Prompt, "<diff>", "</diff>")
		return commitMessage(diff)
	}
	req := o.Prompt
	if _, after, ok := strings.Cut(req, "<request>"); ok {
		req, _, _ = strings.Cut(after, "</request>")
	}
	if strings.Contains(req, "fail") {
		return "", errors.New("fake: naming failed")
	}
	if strings.Contains(req, "slow") {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	words := strings.Fields(req)
	words = words[:min(len(words), 4)]
	b, _ := json.Marshal(map[string]string{
		"title":  "Named: " + strings.Join(words, " "),
		"branch": "named-" + strings.ToLower(strings.Join(words, "-")),
	})
	return "Here you go:\n" + string(b), nil
}

func between(s, start, end string) (string, bool) {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return "", false
	}
	in, _, _ := strings.Cut(after, end)
	return in, true
}

// commitMessage names the files in the diff's "diff --git a/<path> b/<path>"
// lines, or fails when a file's name says "fail".
func commitMessage(diff string) (string, error) {
	var files []string
	for _, line := range strings.Split(diff, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			name, _, _ := strings.Cut(rest, " b/")
			files = append(files, name)
		}
	}
	if len(files) == 0 || strings.Contains(strings.Join(files, " "), "fail") {
		return "", errors.New("fake: writing the commit message failed")
	}
	return "Change " + strings.Join(files, ", ") + "\n\nWritten by the fake system agent.", nil
}

// pullRequest titles the branch after its latest commit, "- <subject>", and
// lists the commits.
func pullRequest(commits string) (string, error) {
	var subjects []string
	for _, line := range strings.Split(commits, "\n") {
		if s, ok := strings.CutPrefix(line, "- "); ok {
			subjects = append(subjects, s)
		}
	}
	if len(subjects) == 0 || strings.Contains(subjects[0], "fail") {
		return "", errors.New("fake: writing the pull request failed")
	}
	b, _ := json.Marshal(map[string]string{
		"title": "Fake: " + subjects[0],
		"body":  "Commits:\n\n- " + strings.Join(subjects, "\n- "),
	})
	return "```json\n" + string(b) + "\n```", nil
}

// UpdateCommand pretends to download an update, with a progress line that
// redraws itself.
func (Harness) UpdateCommand() (harness.Command, bool) {
	return harness.Command{Args: []string{"/bin/sh", "-c", `echo "Checking for updates to Fake…"; sleep 0.5
printf "Downloading  50%%\r"; sleep 0.5; printf "Downloading 100%%\n"; echo "Fake is up to date."`}}, true
}

// LoginCommand prints a sign-in link and asks for a code: an empty one
// fails.
func (Harness) LoginCommand() (harness.Command, bool) {
	return harness.Command{Args: []string{"/bin/sh", "-c", `echo "Open this link to sign in: https://example.com/fake-login?session=42"
printf "Paste code here > "; read code
if [ -z "$code" ]; then echo "No code given."; exit 1; fi
echo "Signed in to Fake."`}}, true
}
