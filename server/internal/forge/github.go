package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"remote-agent/internal/model"
)

// GitHub finds pull requests with gh, signed in on the computer, as the
// commits rad makes open them (orchestrator/commit.go).
type GitHub struct {
	Run Runner
}

func (*GitHub) ID() string   { return "github" }
func (*GitHub) Name() string { return "GitHub" }

// Hosts takes github.com, GitHub Enterprise Cloud's ghe.com, and ssh host
// aliases that name GitHub (github.com-work), which gh resolves itself.
func (*GitHub) Hosts(r Remote) bool {
	return strings.Contains(r.Host, "github") || strings.HasSuffix(r.Host, ".ghe.com")
}

// lookPath finds gh; tests replace it.
var lookPath = exec.LookPath

// ghFields are what PullRequest asks gh for.
const ghFields = "number,title,url,state,isDraft,headRefName,baseRefName,reviewDecision,statusCheckRollup"

type ghPullRequest struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"` // OPEN, CLOSED, MERGED
	IsDraft        bool   `json:"isDraft"`
	HeadRefName    string `json:"headRefName"`
	BaseRefName    string `json:"baseRefName"`
	ReviewDecision string `json:"reviewDecision"`
	// Check runs (status, conclusion) and commit statuses (state).
	StatusCheckRollup []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

func (g *GitHub) PullRequest(ctx context.Context, dir, branch string) (*model.PullRequest, error) {
	gh, err := lookPath("gh")
	if err != nil {
		return nil, ErrUnavailable
	}
	// Newest first; a branch's merged pull request may have a newer open one.
	out, err := g.Run(ctx, dir, gh, "pr", "list", "--head", branch, "--state", "all", "--limit", "10", "--json", ghFields)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 4 {
			return nil, ErrSignedOut
		}
		return nil, err
	}
	var prs []ghPullRequest
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var pick *ghPullRequest
	for i := range prs {
		p := &prs[i]
		if p.HeadRefName != branch {
			continue
		}
		if p.State == "OPEN" {
			pick = p
			break
		}
		if pick == nil {
			pick = p
		}
	}
	if pick == nil {
		return nil, nil
	}
	return g.convert(pick), nil
}

func (g *GitHub) convert(p *ghPullRequest) *model.PullRequest {
	pr := &model.PullRequest{Forge: g.ID(), Number: p.Number, Title: p.Title, URL: p.URL, Head: p.HeadRefName, Base: p.BaseRefName}
	switch {
	case p.State == "MERGED":
		pr.State = model.PullRequestMerged
	case p.State == "CLOSED":
		pr.State = model.PullRequestClosed
	case p.IsDraft:
		pr.State = model.PullRequestDraft
	default:
		pr.State = model.PullRequestOpen
	}
	switch p.ReviewDecision {
	case "APPROVED":
		pr.Review = model.ReviewApproved
	case "CHANGES_REQUESTED":
		pr.Review = model.ReviewChangesRequested
	case "REVIEW_REQUIRED":
		pr.Review = model.ReviewRequired
	}
	failing, pending := false, false
	for _, c := range p.StatusCheckRollup {
		switch {
		case c.State != "": // a commit status
			switch c.State {
			case "FAILURE", "ERROR":
				failing = true
			case "PENDING", "EXPECTED":
				pending = true
			}
		case c.Status != "COMPLETED":
			pending = true
		default:
			switch c.Conclusion {
			case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
				failing = true
			}
		}
	}
	switch {
	case failing:
		pr.Checks = model.ChecksFailing
	case pending:
		pr.Checks = model.ChecksPending
	case len(p.StatusCheckRollup) > 0:
		pr.Checks = model.ChecksPassing
	}
	return pr
}
