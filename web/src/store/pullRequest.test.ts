import { describe, expect, it } from "vitest";
import type { PullRequest } from "../protocol/models";
import {
  forgeName,
  pullRequestAccessibilityLabel,
  pullRequestChecks,
  pullRequestReview,
  pullRequestState,
  pullRequestStateLabel,
  pullRequestStatusLine,
  pullRequestTooltip,
} from "./pullRequest";

const base: PullRequest = {
  forge: "github",
  number: 42,
  title: "Fix the flaky tests",
  url: "https://github.com/acme/app/pull/42",
  state: "open",
  head: "ra/fix-tests",
  base: "main",
};

describe("pull request wording", () => {
  it("labels each state", () => {
    expect((["open", "draft", "merged", "closed"] as const).map((state) => pullRequestStateLabel({ ...base, state }))).toEqual([
      "Open",
      "Draft",
      "Merged",
      "Closed",
    ]);
  });

  it("joins the state, checks and review, leaving out the absent ones", () => {
    expect(pullRequestStatusLine({ ...base, checks: "failing", review: "changes_requested" })).toBe("Open · Checks failing · Changes requested");
    expect(pullRequestStatusLine({ ...base, state: "merged", checks: "passing", review: "approved" })).toBe("Merged · Checks passing · Approved");
    expect(pullRequestStatusLine({ ...base, state: "draft", checks: "pending" })).toBe("Draft · Checks running");
    expect(pullRequestStatusLine({ ...base, review: "review_required" })).toBe("Open · Review required");
    expect(pullRequestStatusLine({ ...base, state: "closed" })).toBe("Closed");
  });

  it("says it all for a screen reader", () => {
    expect(pullRequestAccessibilityLabel({ ...base, checks: "failing", review: "changes_requested" })).toBe(
      "Pull request #42, Open, Checks failing, Changes requested",
    );
    expect(pullRequestAccessibilityLabel({ ...base, state: "closed" })).toBe("Pull request #42, Closed");
  });

  it("puts the number and title over the status line in the tooltip", () => {
    expect(pullRequestTooltip({ ...base, checks: "passing" })).toBe("#42 Fix the flaky tests\nOpen · Checks passing");
  });

  it("reads unknown values leniently", () => {
    const pr = { ...base, state: "queued", checks: "skipped", review: "dismissed" };
    expect(pullRequestState(pr)).toBe("open");
    expect(pullRequestChecks(pr)).toBeUndefined();
    expect(pullRequestReview(pr)).toBeUndefined();
    expect(pullRequestStatusLine(pr)).toBe("Open");
  });

  it("names the forge, or the url's host", () => {
    expect(forgeName(base)).toBe("GitHub");
    expect(forgeName({ ...base, forge: "gitlab", url: "https://gitlab.example.com/a/b/-/merge_requests/3" })).toBe("gitlab.example.com");
    expect(forgeName({ ...base, forge: "gitea", url: "not a url" })).toBe("gitea");
  });
});
