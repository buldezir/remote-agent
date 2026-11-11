package client

import (
	"path/filepath"

	"remote-agent/internal/model"
)

// The new session form's choices of where to work, as the apps' NewSessionView.

// AvailableBranches are the branches a new worktree can check out: those
// no checkout has, neither the project folder (current) nor a worktree, in
// the server's order.
func AvailableBranches(branches []string, current string, worktrees []model.WorktreeInfo) []string {
	taken := map[string]bool{current: true}
	for _, wt := range worktrees {
		if wt.Branch != "" {
			taken[wt.Branch] = true
		}
	}
	var out []string
	for _, b := range branches {
		if !taken[b] {
			out = append(out, b)
		}
	}
	return out
}

// WorktreeLabel names an existing worktree in the new session form: its
// branch, else its folder's name, detached.
func WorktreeLabel(wt model.WorktreeInfo) string {
	if wt.Branch != "" {
		return wt.Branch
	}
	return filepath.Base(wt.Path) + " (detached)"
}
