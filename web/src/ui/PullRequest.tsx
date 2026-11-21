import { Check, Copy, ExternalLink, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, X } from "lucide-react";
import type { PullRequest } from "../protocol/models";
import {
  forgeName,
  pullRequestAccessibilityLabel,
  pullRequestChecks,
  pullRequestState,
  pullRequestStatusLine,
  pullRequestTooltip,
} from "../store/pullRequest";
import { useContextMenu, type MenuItem } from "./primitives";
import { openLink } from "./TaskSheet";

const glyphs = { open: GitPullRequest, draft: GitPullRequestDraft, merged: GitMerge, closed: GitPullRequestClosed };

/** The glyph of a pull request's state; its parent tints it. */
function PullRequestGlyph({ pr, size }: { pr: PullRequest; size: number }) {
  const Glyph = glyphs[pullRequestState(pr)];
  return <Glyph size={size} aria-hidden />;
}

/** A session row's pull request: its glyph and number, tinted by its state.
 *  Display only, so the row stays one target; it tells more on hover. */
export function PullRequestBadge({ pr }: { pr: PullRequest }) {
  return (
    <span className={`pr-badge pr-${pullRequestState(pr)}`} title={pullRequestTooltip(pr)} role="img" aria-label={pullRequestAccessibilityLabel(pr)}>
      <PullRequestGlyph pr={pr} size={11} />
      <span>#{pr.number}</span>
    </span>
  );
}

/** The session bar's pull request: its glyph and number, tinted by its
 *  state, then its checks. A click opens it on its forge; its context menu
 *  has its title and status, and opens it or copies its link. */
export function PullRequestButton({ pr }: { pr: PullRequest }) {
  const checks = pullRequestChecks(pr);
  const { onContextMenu, menu } = useContextMenu([
    {
      section: (
        <span className="pr-menu-heading">
          <span className="pr-menu-title">
            #{pr.number} {pr.title}
          </span>
          <span className="pr-menu-status">{pullRequestStatusLine(pr)}</span>
        </span>
      ),
      items: ([
        { label: `Open on ${forgeName(pr)}`, icon: ExternalLink, onSelect: () => openLink(pr.url) },
        navigator.clipboard?.writeText && { label: "Copy Link", icon: Copy, onSelect: () => void navigator.clipboard.writeText(pr.url).catch(() => {}) },
      ] as (MenuItem | undefined)[]).filter((i): i is MenuItem => !!i),
    },
  ]);
  return (
    <>
      <button
        className={`pr-button pr-${pullRequestState(pr)}`}
        onClick={() => openLink(pr.url)}
        onContextMenu={onContextMenu}
        title={pullRequestTooltip(pr)}
        aria-label={pullRequestAccessibilityLabel(pr)}
      >
        <PullRequestGlyph pr={pr} size={14} />
        <span>#{pr.number}</span>
        {checks === "passing" && <Check size={12} strokeWidth={3} className="c-green" aria-hidden />}
        {checks === "failing" && <X size={12} strokeWidth={3} className="c-red" aria-hidden />}
        {checks === "pending" && <span className="pr-checks-pending" aria-hidden />}
      </button>
      {menu}
    </>
  );
}
