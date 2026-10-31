// The vectors every client's TranscriptFind shares (RAKit, android/core, ra).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { Event, Item, JSONValue } from "../protocol/models";
import { findMatches, searchText, toolCommand } from "./transcriptFind";

const fixture = join(import.meta.dirname, "../../../protocol/fixtures/notification_events_session.json");
const events: Event[] = JSON.parse(readFileSync(fixture, "utf8")).params.events;
const items = events.flatMap((e) => (e.item ? [e.item] : []));

const tool = (title: string | undefined, input?: JSONValue): Item => ({
  id: "t",
  sessionId: "s",
  order: 1,
  kind: "tool_call",
  status: "completed",
  tool: { name: "Bash", kind: "execute", title, input },
  createdAt: "2026-10-09T12:00:00Z",
  updatedAt: "2026-10-09T12:00:00Z",
});

describe("TranscriptFind", () => {
  it("finds items by their text, in the transcript's order", () => {
    expect(findMatches(items, "TESTS")).toEqual(["i1", "i4", "i8"]);
    expect(findMatches(items, "which")).toEqual(["i6"]);
    expect(findMatches(items, "go test")).toEqual(["i3"]);
    // Reasoning isn't searched, nor are sub-agents' items.
    expect(findMatches(items, "test output")).toEqual([]);
    expect(findMatches(items, "sub-agent")).toEqual([]);
    expect(findMatches(items, "")).toEqual([]);
    expect(findMatches(items, "   ")).toEqual([]);
    // Trimmed, but the spaces inside count.
    expect(findMatches(items, "  which db  ")).toEqual(["i6"]);
    expect(findMatches(items, "which  db")).toEqual([]);
  });

  it("reads a tool call's title and command", () => {
    expect(searchText(tool("Run", { command: ["git", "status"] }))).toBe("Run\ngit status");
    expect(searchText(tool("Run git status", { command: "git status" }))).toBe("Run git status");
    expect(searchText(tool("Run"))).toBe("Run");
    expect(searchText(tool(undefined, { command: "ls" }))).toBe("\nls");
    expect(toolCommand({ command: ["a", 1] })).toBe("");
    expect(toolCommand({ command: 3 })).toBe("");
    expect(toolCommand(["command"])).toBe("");
    expect(toolCommand("ls")).toBe("");
    expect(toolCommand(null)).toBe("");
  });

  it("reads plans, proposed plans and questions", () => {
    const byId = new Map(items.map((i) => [i.id, i]));
    expect(searchText(byId.get("i8")!)).toBe("Run tests\nFix bug");
    expect(searchText(byId.get("i7")!)).toBe("1. Do it");
    expect(searchText(byId.get("i6")!)).toBe("Which DB?\nWhich port?");
    // A tool call's approval has its tool call's title, which is searched instead.
    expect(searchText(byId.get("i5")!)).toBe("");
    expect(searchText(byId.get("i2")!)).toBe("");
    expect(searchText(byId.get("i11")!)).toBe("");
    expect(searchText(byId.get("i3")!)).toBe("go test ./...");
    const plan: Item = { ...byId.get("i8")!, plan: { entries: [{ content: "Step", status: "pending" }], text: "Why" } };
    expect(searchText(plan)).toBe("Step\nWhy");
  });
});
