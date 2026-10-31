// Find in a session's transcript, over the text rad's search reads: a port
// of SearchText and ToolCommand in server/internal/model/search.go, as
// RAKit's TranscriptFind.
import type { Item, JSONValue } from "../protocol/models";

/** The text of an item that find and rad's search look in: prompts,
 *  replies, notices and errors, a tool call's title and command, a plan,
 *  and a proposed plan's or a question's text. Reasoning, tool output,
 *  approvals of tool calls and sub-agents' items aren't searched. */
export function searchText(item: Item): string {
  if (item.parentItemId) return "";
  switch (item.kind) {
    case "user_message":
    case "assistant_message":
    case "notice":
    case "error":
      return item.text ?? "";
    case "tool_call": {
      if (!item.tool) return "";
      let s = item.tool.title ?? "";
      const cmd = toolCommand(item.tool.input);
      if (cmd !== "" && !s.includes(cmd)) s += "\n" + cmd;
      return s;
    }
    case "plan": {
      if (!item.plan) return "";
      const parts = (item.plan.entries ?? []).map((e) => e.content);
      if (item.plan.text) parts.push(item.plan.text);
      return parts.join("\n");
    }
    case "approval": {
      const a = item.approval;
      if (!a) return "";
      if (a.special === "plan") return a.planText ?? "";
      if (a.special === "question") return (a.questions ?? []).map((q) => q.question).join("\n");
      return "";
    }
    default:
      return "";
  }
}

/** The command a tool call ran, from its input's "command": a string, or a
 *  list of arguments, which it joins with spaces. */
export function toolCommand(input: JSONValue | undefined): string {
  if (!input || typeof input !== "object" || Array.isArray(input)) return "";
  const c = input.command;
  if (typeof c === "string") return c;
  if (Array.isArray(c) && c.every((a) => typeof a === "string")) return c.join(" ");
  return "";
}

/** The ids of the items whose text has the query, in the order given (the
 *  transcript's): case-insensitively, the whole query as typed, trimmed. */
export function findMatches(items: Item[], query: string): string[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [];
  return items.filter((it) => searchText(it).toLowerCase().includes(q)).map((it) => it.id);
}
