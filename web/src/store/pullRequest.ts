// How a session's pull request reads, the same in every client: its state,
// checks and review, the status line they make, and the label a screen
// reader hears. Views take their colours and glyphs from `pullRequestState`
// and `pullRequestChecks`, which read unknown values leniently.
import type { PullRequest } from "../protocol/models";

export type KnownPullRequestState = "open" | "draft" | "merged" | "closed";
export type KnownPullRequestChecks = "passing" | "failing" | "pending";
export type KnownPullRequestReview = "approved" | "changes_requested" | "review_required";

const states: readonly string[] = ["open", "draft", "merged", "closed"] satisfies KnownPullRequestState[];
const checks: readonly string[] = ["passing", "failing", "pending"] satisfies KnownPullRequestChecks[];
const reviews: readonly string[] = ["approved", "changes_requested", "review_required"] satisfies KnownPullRequestReview[];

/** The state to show: one this client doesn't know shows as open. */
export function pullRequestState(pr: PullRequest): KnownPullRequestState {
  return states.includes(pr.state) ? (pr.state as KnownPullRequestState) : "open";
}

/** The checks to show: absent, or ones this client doesn't know, show as none. */
export function pullRequestChecks(pr: PullRequest): KnownPullRequestChecks | undefined {
  return pr.checks && checks.includes(pr.checks) ? (pr.checks as KnownPullRequestChecks) : undefined;
}

/** The review to show: absent, or one this client doesn't know, shows as none. */
export function pullRequestReview(pr: PullRequest): KnownPullRequestReview | undefined {
  return pr.review && reviews.includes(pr.review) ? (pr.review as KnownPullRequestReview) : undefined;
}

/** "Open", "Draft", "Merged" or "Closed". */
export function pullRequestStateLabel(pr: PullRequest): string {
  return { open: "Open", draft: "Draft", merged: "Merged", closed: "Closed" }[pullRequestState(pr)];
}

/** "Checks passing", "Checks failing" or "Checks running"; none without checks. */
export function pullRequestChecksLabel(pr: PullRequest): string | undefined {
  const c = pullRequestChecks(pr);
  return c && { passing: "Checks passing", failing: "Checks failing", pending: "Checks running" }[c];
}

/** "Approved", "Changes requested" or "Review required"; none without a review. */
export function pullRequestReviewLabel(pr: PullRequest): string | undefined {
  const review = pullRequestReview(pr);
  return review && { approved: "Approved", changes_requested: "Changes requested", review_required: "Review required" }[review];
}

function parts(pr: PullRequest): string[] {
  return [pullRequestStateLabel(pr), pullRequestChecksLabel(pr), pullRequestReviewLabel(pr)].filter((p): p is string => !!p);
}

/** "Open · Checks failing · Changes requested": the state, checks and
 *  review, the absent ones left out. */
export function pullRequestStatusLine(pr: PullRequest): string {
  return parts(pr).join(" · ");
}

/** "Pull request #42, Open, Checks failing, Changes requested". */
export function pullRequestAccessibilityLabel(pr: PullRequest): string {
  return [`Pull request #${pr.number}`, ...parts(pr)].join(", ");
}

/** "#42 Fix the flaky tests", then the status line on a line of its own:
 *  the tooltip. */
export function pullRequestTooltip(pr: PullRequest): string {
  return `#${pr.number} ${pr.title}\n${pullRequestStatusLine(pr)}`;
}

/** The forge's name: "GitHub" for github, else the url's host. */
export function forgeName(pr: PullRequest): string {
  if (pr.forge === "github") return "GitHub";
  try {
    return new URL(pr.url).host || pr.forge;
  } catch {
    return pr.forge;
  }
}
