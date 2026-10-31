// The vectors every client's ToolRuns shares (RAKit, android/core, ra).
import { describe, expect, it } from "vitest";
import type { Item, ItemKind, ItemStatus, ToolKind, Turn, TurnStatus } from "../protocol/models";
import { runContaining, runParts, runTitle, runTools, runWorking, toolRuns, transcriptRows, type TranscriptRow } from "./toolRuns";

const item = (id: string, kind: ItemKind, turn: string | undefined = "t1", status: ItemStatus = "completed"): Item => ({
  id,
  sessionId: "s",
  turnId: turn,
  order: 0,
  kind,
  status,
  createdAt: "2026-10-09T12:00:00Z",
  updatedAt: "2026-10-09T12:00:00Z",
});

const tool = (id: string, kind: ToolKind, { turn = "t1", status = "completed", paths }: { turn?: string; status?: ItemStatus; paths?: string[] } = {}): Item => ({
  ...item(id, "tool_call", turn, status),
  tool: { name: id, kind, title: id, paths },
});

const turn = (id: string, status: TurnStatus): Turn => ({ id, sessionId: "s", n: 1, status, startedAt: "2026-10-09T12:00:00Z" });

/** A finished turn with a run, a failed command, a lone edit and a reply,
 *  and a running one with a run still going. */
const transcript: Item[] = [
  item("u1", "user_message"),
  item("r1", "reasoning"),
  tool("a", "execute"),
  tool("b", "read", { paths: ["README.md"] }),
  item("r2", "reasoning"),
  tool("c", "search"),
  tool("d", "read", { paths: ["README.md"] }),
  tool("e", "edit", { paths: ["main.go"] }),
  tool("f", "edit", { paths: ["main.go", "main_test.go"] }),
  tool("g", "execute", { status: "failed" }),
  tool("h", "edit", { paths: ["main.go"] }),
  item("m1", "assistant_message"),
  item("u2", "user_message", "t2"),
  tool("i", "fetch", { turn: "t2" }),
  tool("j", "think", { turn: "t2" }),
  tool("k", "other", { turn: "t2", status: "in_progress" }),
];

const turns = new Map([
  ["t1", turn("t1", "completed")],
  ["t2", turn("t2", "running")],
]);

const ids = (rows: TranscriptRow[]) =>
  rows.map((r) => (r.kind === "item" ? r.item.id : r.kind === "run" ? `${r.run.id} ${r.expanded ? "open" : "shut"}` : `end ${r.turn.id}`));

describe("ToolRuns", () => {
  it("folds runs of tool calls", () => {
    const runs = toolRuns(transcript, turns);
    expect(runs.map((r) => r.id)).toEqual(["tools:r1", "tools:i"]);
    expect(runs[0].items.map((i) => i.id)).toEqual(["r1", "a", "b", "r2", "c", "d", "e", "f"]);
    expect(runTools(runs[0]).length).toBe(6);
    expect(runs[0].live).toBe(false);
    expect(runWorking(runs[0])).toBe(false);
    const parts = runParts(runTools(runs[0]));
    expect(runTitle(parts)).toBe("Ran 1 command, edited 2 files, read 1 file, searched 1 time");
    expect(parts.map((p) => p.kind)).toEqual(["execute", "edit", "read", "search"]);
    expect(parts.map((p) => p.count)).toEqual([1, 2, 1, 1]);
    // Its turn runs and nothing came after it.
    expect(runs[1].items.map((i) => i.id)).toEqual(["i", "j", "k"]);
    expect(runs[1].live).toBe(true);
    expect(runWorking(runs[1])).toBe(true);
    expect(runTitle(runParts(runTools(runs[1])))).toBe("Fetched 1 page, started 1 agent, used 1 tool");
  });

  it("shows a run shut unless it's live", () => {
    expect(ids(transcriptRows(transcript, turns))).toEqual(["u1", "tools:r1 shut", "g", "h", "m1", "u2", "tools:i open", "i", "j", "k"]);
    // The user's choice wins, either way.
    expect(
      ids(
        transcriptRows(
          transcript,
          turns,
          new Map([
            ["tools:r1", true],
            ["tools:i", false],
          ]),
        ),
      ),
    ).toEqual(["u1", "tools:r1 open", "r1", "a", "b", "r2", "c", "d", "e", "f", "g", "h", "m1", "u2", "tools:i shut"]);
    // Not grouping: every item is a row.
    expect(ids(transcriptRows(transcript, turns, new Map(), false))).toEqual(transcript.map((i) => i.id));
  });

  it("folds a live run once the agent replies or the turn ends", () => {
    const items = [...transcript];
    expect(toolRuns(items, turns)[1].live).toBe(true);
    // A prompt queued meanwhile isn't the turn's.
    items.push(item("q", "user_message", undefined, "queued"));
    expect(toolRuns(items, turns)[1].live).toBe(true);
    items.push(item("m2", "assistant_message", "t2", "in_progress"));
    expect(toolRuns(items, turns)[1].live).toBe(false);
    expect(ids(transcriptRows(items, turns)).slice(-4)).toEqual(["u2", "tools:i shut", "q", "m2"]);
    const ended = new Map(turns);
    ended.set("t2", turn("t2", "completed"));
    expect(toolRuns(transcript, ended)[1].live).toBe(false);
  });

  it("leaves calls that need attention on their own", () => {
    const shot = { ...tool("s", "other"), images: [{ id: "img", mimeType: "image/png", size: 1 }] };
    const items = [tool("a", "execute"), tool("b", "edit", { status: "pending" }), tool("c", "execute"), shot, tool("d", "read"), item("r", "reasoning"), tool("e", "read")];
    // Pending and with images, a call breaks the run; one call alone
    // doesn't fold, and reasoning doesn't count.
    expect(ids(transcriptRows(items, turns))).toEqual(["a", "b", "c", "s", "tools:d shut"]);
    expect(runTitle(runParts(runTools(toolRuns(items, turns)[0])))).toBe("Read 2 files");
    // Nor does reasoning on its own.
    expect(toolRuns([item("r1", "reasoning"), item("r2", "reasoning"), tool("x", "read")], turns)).toEqual([]);
  });

  it("keeps runs within a turn", () => {
    const items = [tool("a", "execute"), tool("b", "execute", { turn: "t2" }), tool("c", "execute", { turn: "t2" })];
    expect(ids(transcriptRows(items, turns))).toEqual(["a", "tools:b open", "b", "c"]);
  });

  it("ends a turn that ended badly after its run", () => {
    const items = [item("u", "user_message"), tool("a", "execute"), tool("b", "read")];
    const failed = new Map([["t1", turn("t1", "failed")]]);
    expect(ids(transcriptRows(items, failed))).toEqual(["u", "tools:a shut", "end t1"]);
    expect(ids(transcriptRows(items, failed, new Map([["tools:a", true]])))).toEqual(["u", "tools:a open", "a", "b", "end t1"]);
    expect(ids(transcriptRows(items, new Map([["t1", turn("t1", "interrupted")]]), new Map(), false))).toEqual(["u", "a", "b", "end t1"]);
  });

  it("finds the run an item is in", () => {
    expect(runContaining("c", transcript, turns)?.id).toBe("tools:r1");
    expect(runContaining("r2", transcript, turns)?.id).toBe("tools:r1");
    expect(runContaining("g", transcript, turns)).toBeUndefined();
    expect(runContaining("m1", transcript, turns)).toBeUndefined();
  });

  it("counts files by path and the rest by call", () => {
    const items = [
      tool("a", "read"),
      tool("b", "read"),
      tool("c", "edit", { paths: ["x"] }),
      tool("d", "edit", { paths: ["x"] }),
      tool("e", "search"),
      tool("f", "search"),
      tool("g", "fetch"),
      tool("h", "fetch"),
      tool("i", "think"),
      tool("j", "think"),
      tool("k", "other"),
      tool("l", "other"),
      tool("m", "execute"),
      tool("n", "execute"),
    ];
    expect(runTitle(runParts(items))).toBe("Ran 2 commands, edited 1 file, read 2 files, searched 2 times, fetched 2 pages, started 2 agents, used 2 tools");
    expect(runTitle([])).toBe("");
    // A kind this client doesn't know is a tool.
    expect(runTitle(runParts([tool("z", "delete" as ToolKind)]))).toBe("Used 1 tool");
  });
});
