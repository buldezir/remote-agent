import { describe, expect, it } from "vitest";
import { occurrences } from "./findHighlight";

describe("occurrences", () => {
  it("finds the query in a node's text, case-insensitively", () => {
    expect(occurrences(["All tests pass, tests too"], "TESTS")).toEqual([
      { start: [0, 4], end: [0, 9] },
      { start: [0, 16], end: [0, 21] },
    ]);
    expect(occurrences(["abc"], "")).toEqual([]);
    expect(occurrences(["abc"], "  ")).toEqual([]);
    expect(occurrences(["abc"], "x")).toEqual([]);
    expect(occurrences([], "x")).toEqual([]);
  });

  it("spans nodes, as Markdown splits its text", () => {
    // "All **tests** pass now."
    const runs = ["All ", "tests", " pass now."];
    expect(occurrences(runs, " tests pass ")).toEqual([{ start: [1, 0], end: [2, 5] }]);
    // Ending where a node ends stays in it.
    expect(occurrences(runs, "all tests")).toEqual([{ start: [0, 0], end: [1, 5] }]);
    expect(occurrences(runs, "now.")).toEqual([{ start: [2, 6], end: [2, 10] }]);
    // Empty nodes are skipped over.
    expect(occurrences(["a", "", "b", ""], "ab")).toEqual([{ start: [0, 0], end: [2, 1] }]);
    expect(occurrences(["", "ab"], "ab")).toEqual([{ start: [1, 0], end: [1, 2] }]);
  });

  it("takes the query's characters as they are", () => {
    expect(occurrences(["run go test ./... (all)"], "./...")).toEqual([{ start: [0, 12], end: [0, 17] }]);
    expect(occurrences(["a(b)"], "(b)")).toEqual([{ start: [0, 1], end: [0, 4] }]);
    expect(occurrences(["a.b"], "a*b")).toEqual([]);
  });
});
