// The vectors every client's @ session mentions share (RAKit, android/core,
// gpui), read from protocol/vectors as usageLimits.test.ts reads its own.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { completeMention, mentionDisplay, mentionQuery, mentions, mentionSuggestions, mentionText, type MentionCandidate } from "./sessionMentions";

interface Vectors {
  self: string;
  sessions: MentionCandidate[];
  query: { text: string; query: string | null }[];
  suggestions: { query: string; ids: string[] }[];
  complete: { text: string; id: string; result: string }[];
  mentionText: { title: string; id: string; text: string }[];
  display: { text: string; display: string }[];
}

const v: Vectors = JSON.parse(readFileSync(join(import.meta.dirname, "../../../protocol/vectors/session_mentions.json"), "utf8"));

/** A text whose `|` marks the cursor, without it, and the cursor. */
const marked = (s: string): [string, number] => [s.replace("|", ""), s.indexOf("|")];

describe("session mentions vectors", () => {
  it("has cases of each kind", () => {
    for (const list of [v.query, v.suggestions, v.complete, v.mentionText, v.display]) expect(list.length).toBeGreaterThan(0);
  });

  it.each(v.query)("finds the query in $text", ({ text, query }) => {
    const [t, at] = marked(text);
    expect(mentionQuery(t, at)?.query ?? null).toBe(query);
  });

  it.each(v.suggestions)("suggests sessions for '$query'", ({ query, ids }) => {
    expect(mentionSuggestions(query, v.sessions, v.self).map((s) => s.id)).toEqual(ids);
  });

  it.each(v.complete)("completes $text with $id", ({ text, id, result }) => {
    const [t, at] = marked(text);
    const q = mentionQuery(t, at)!;
    const s = v.sessions.find((s) => s.id === id)!;
    const r = completeMention(t, at, q, s.title, s.id);
    expect(r.text.slice(0, r.cursor) + "|" + r.text.slice(r.cursor)).toBe(result);
  });

  it.each(v.mentionText)("writes the mention of '$title'", ({ title, id, text }) => {
    expect(mentionText(title, id)).toBe(text);
  });

  it.each(v.display)("shows $text", ({ text, display }) => {
    expect(mentionDisplay(text)).toBe(display);
  });
});

describe("mentions", () => {
  it("finds well-formed mentions only", () => {
    const text = "a @[Fix](session:s-1) b @[x](other:1) @[y](session:) @[Ünï](session:s-2)";
    const m = mentions(text);
    expect(m).toHaveLength(2);
    expect(text.slice(m[0].start, m[0].end)).toBe("@[Fix](session:s-1)");
    expect([m[1].title, m[1].id]).toEqual(["Ünï", "s-2"]);
  });

  it("lists at most eight sessions", () => {
    const many = Array.from({ length: 12 }, (_, i) => ({ id: `s${i}`, title: `Session ${i}`, updatedAt: "2026-11-22T10:00:00Z" }));
    expect(mentionSuggestions("", many, "s0")).toHaveLength(8);
  });
});
