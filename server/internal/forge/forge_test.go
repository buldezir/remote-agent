package forge

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"remote-agent/internal/model"
)

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]Remote{
		"https://github.com/acme/app.git":          {"github.com", "acme/app"},
		"https://user:tok@GitHub.com/acme/app/":    {"github.com", "acme/app"},
		"git@github.com:acme/app.git":              {"github.com", "acme/app"},
		"ssh://git@ssh.github.com:443/acme/app":    {"ssh.github.com", "acme/app"},
		"github.com-work:acme/app.git":             {"github.com-work", "acme/app"},
		"https://gitlab.example.com/g/sub/app.git": {"gitlab.example.com", "g/sub/app"},
	} {
		if got, ok := ParseRemote(in); !ok || got != want {
			t.Errorf("ParseRemote(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"/tmp/origin.git", "file:///tmp/origin.git", "../origin", "", "./a:b/c"} {
		if got, ok := ParseRemote(in); ok {
			t.Errorf("ParseRemote(%q) = %v; want none", in, got)
		}
	}
}

func TestFor(t *testing.T) {
	if f := For("git@github.com:acme/app.git", nil); f == nil || f.ID() != "github" {
		t.Fatalf("github remote: %v", f)
	}
	if f := For("https://acme.ghe.com/acme/app", nil); f == nil || f.ID() != "github" {
		t.Fatalf("ghe.com remote: %v", f)
	}
	for _, u := range []string{"https://gitlab.com/acme/app.git", "/tmp/origin.git"} {
		if f := For(u, nil); f != nil {
			t.Fatalf("For(%q) = %s", u, f.ID())
		}
	}
}

func fakeGH(t *testing.T, out string, err error) (*GitHub, *[]string) {
	t.Helper()
	old := lookPath
	lookPath = func(string) (string, error) { return "/bin/gh", nil }
	t.Cleanup(func() { lookPath = old })
	var calls []string
	return &GitHub{Run: func(_ context.Context, dir, name string, args ...string) (string, error) {
		calls = append(calls, dir+": "+name+" "+strings.Join(args, " "))
		return out, err
	}}, &calls
}

func TestGitHubPicksOpenElseLatest(t *testing.T) {
	g, calls := fakeGH(t, `[
		{"number":9,"title":"Other fork","url":"u9","state":"OPEN","headRefName":"main"},
		{"number":8,"title":"Second go","url":"u8","state":"MERGED","headRefName":"feat","baseRefName":"main"},
		{"number":7,"title":"Again","url":"u7","state":"OPEN","isDraft":true,"headRefName":"feat","baseRefName":"main",
		 "reviewDecision":"REVIEW_REQUIRED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"IN_PROGRESS"}]},
		{"number":5,"title":"First","url":"u5","state":"CLOSED","headRefName":"feat"}
	]`, nil)
	pr, err := g.PullRequest(context.Background(), "/repo", "feat")
	if err != nil {
		t.Fatal(err)
	}
	want := model.PullRequest{Forge: "github", Number: 7, Title: "Again", URL: "u7", State: model.PullRequestDraft, Head: "feat", Base: "main",
		Checks: model.ChecksPending, Review: model.ReviewRequired}
	if pr == nil || *pr != want {
		t.Fatalf("got %+v, want %+v", pr, want)
	}
	if len(*calls) != 1 || (*calls)[0] != "/repo: /bin/gh pr list --head feat --state all --limit 10 --json "+ghFields {
		t.Fatalf("calls %q", *calls)
	}

	g, _ = fakeGH(t, `[{"number":8,"title":"Done","url":"u8","state":"MERGED","headRefName":"feat",
		"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"state":"SUCCESS"},{"status":"COMPLETED","conclusion":"SKIPPED"}]},
		{"number":5,"state":"CLOSED","headRefName":"feat"}]`, nil)
	if pr, _ := g.PullRequest(context.Background(), "/repo", "feat"); pr == nil || pr.Number != 8 || pr.State != model.PullRequestMerged || pr.Checks != model.ChecksPassing {
		t.Fatalf("merged: %+v", pr)
	}

	g, _ = fakeGH(t, `[{"number":3,"state":"OPEN","headRefName":"feat","reviewDecision":"CHANGES_REQUESTED",
		"statusCheckRollup":[{"state":"PENDING"},{"status":"COMPLETED","conclusion":"FAILURE"}]}]`, nil)
	if pr, _ := g.PullRequest(context.Background(), "/repo", "feat"); pr == nil || pr.State != model.PullRequestOpen || pr.Checks != model.ChecksFailing || pr.Review != model.ReviewChangesRequested {
		t.Fatalf("failing: %+v", pr)
	}

	g, _ = fakeGH(t, `[]`, nil)
	if pr, err := g.PullRequest(context.Background(), "/repo", "feat"); pr != nil || err != nil {
		t.Fatalf("none: %+v, %v", pr, err)
	}
}

func TestGitHubErrors(t *testing.T) {
	exit4 := exec.Command("sh", "-c", "exit 4").Run()
	g, _ := fakeGH(t, "", exit4)
	if _, err := g.PullRequest(context.Background(), "/repo", "feat"); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("signed out: %v", err)
	}
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := g.PullRequest(context.Background(), "/repo", "feat"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no gh: %v", err)
	}
}
