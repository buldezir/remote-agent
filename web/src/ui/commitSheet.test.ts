import { describe, expect, it } from "vitest";
import type { CommitPreview } from "../protocol/models";
import { commitButtonLabel, commitScope, scopeUnavailable } from "./CommitSheet";

const file = { path: "a.txt", status: "modified", additions: 1, deletions: 0 };
const preview = (p: Partial<CommitPreview>): CommitPreview => ({ branch: "ra/x", remote: "origin", files: [file], message: "Fix", ahead: 0, ...p });

describe("committing", () => {
  it("starts where the preview allows", () => {
    // What was picked, if it can be done.
    expect(commitScope(preview({}), "pullRequest")).toBe("pullRequest");
    expect(commitScope(preview({}), "commit")).toBe("commit");
    // Nowhere to push, or a detached HEAD: only a commit.
    expect(commitScope(preview({ remote: undefined }), "pullRequest")).toBe("commit");
    expect(commitScope(preview({ branch: undefined }), "push")).toBe("commit");
    // Nothing to commit: a push.
    expect(commitScope(preview({ files: [] }), "commit")).toBe("push");
    expect(commitScope(preview({ files: [] }), "pullRequest")).toBe("pullRequest");
    // The default branch opens no pull request: a push instead.
    const onMain = preview({ branch: "main", noPullRequest: "main is the repository's default branch: a pull request needs a branch of its own" });
    expect(commitScope(onMain, "pullRequest")).toBe("push");
    expect(commitScope(onMain, "commit")).toBe("commit");
  });

  it("says why a choice is off", () => {
    expect(["commit", "push", "pullRequest"].map((s) => scopeUnavailable(preview({}), s as "commit"))).toEqual([undefined, undefined, undefined]);
    expect(scopeUnavailable(preview({ files: [] }), "commit")).toBe("Nothing to commit.");
    expect(scopeUnavailable(preview({ remote: undefined }), "push")).toBe("No remote to push to.");
    expect(scopeUnavailable(preview({ branch: undefined }), "pullRequest")).toMatch(/detached/);
    const onMain = preview({ branch: "main", noPullRequest: "main is the repository's default branch: a pull request needs a branch of its own" });
    expect(scopeUnavailable(onMain, "commit")).toBeUndefined();
    expect(scopeUnavailable(onMain, "push")).toBeUndefined();
    expect(scopeUnavailable(onMain, "pullRequest")).toBe("main is the repository's default branch: a pull request needs a branch of its own.");
  });

  it("names the button after what it does", () => {
    expect(commitButtonLabel("commit", true)).toBe("Commit");
    expect(commitButtonLabel("push", true)).toBe("Commit & Push");
    expect(commitButtonLabel("pullRequest", true)).toBe("Commit, Push & Open PR");
    expect(commitButtonLabel("push", false)).toBe("Push");
    expect(commitButtonLabel("pullRequest", false)).toBe("Push & Open PR");
    // A phone has room for less beside the title.
    expect(commitButtonLabel("pullRequest", true, true)).toBe("Commit & Open PR");
    expect(commitButtonLabel("push", true, true)).toBe("Commit & Push");
  });
});
