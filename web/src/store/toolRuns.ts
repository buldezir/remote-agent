// Runs of tool calls in a transcript, each folded into one row that sums it
// up ("Ran 4 commands, edited 3 files, read 2 files") and opens to show the
// calls: a port of RAKit's ToolRuns, as Android's and ra's are.
//
// A run is the tool calls and the agent's thinking between two pieces of
// conversation (a prompt, a reply, an approval, a plan, a notice, an error),
// within one turn. It folds with two tool calls or more. A call that waits
// for approval, failed, or shows images stands on its own, between runs. A
// run is live while its turn runs and nothing has come after it: it shows
// open, unless the user shut it, so that the user can watch the agent work,
// and folds once the agent replies or the turn ends. A prompt queued while
// the agent works isn't conversation yet: it doesn't end the run it was sent
// during, and waits after it.
import type { Item, ToolKind, Turn } from "../protocol/models";

/** The fewest tool calls that fold: one on its own stays as it is. */
export const minimumTools = 2;

export interface ToolRun {
  /** "tools:" and the first item's id, which stays as the run grows. */
  id: string;
  /** Its tool calls, and the reasoning between them. */
  items: Item[];
  /** Its turn runs and nothing has come after it. */
  live: boolean;
}

/** The calls of one kind in a run: "ran 4 commands". */
export interface ToolRunPart {
  kind: ToolKind;
  count: number;
  text: string;
}

/** A row of the transcript: an item, a run's summary (followed by its items
 *  when it's open), or the end of a turn that was interrupted or failed,
 *  after its last item. */
export type TranscriptRow = { kind: "item"; item: Item } | { kind: "run"; run: ToolRun; expanded: boolean } | { kind: "turn"; turn: Turn };

type Segment = { item: Item } | { run: ToolRun };

/** The transcript's rows. A run is open as `expanded` says, by its id, else
 *  while it's live. Without `grouping`, every item is a row. */
export function transcriptRows(
  items: Item[],
  turns: ReadonlyMap<string, Turn>,
  expanded: ReadonlyMap<string, boolean> = new Map(),
  grouping = true,
): TranscriptRow[] {
  // A prompt queued during a turn sits among its items, so the last is looked up.
  const last = new Map<string, string>();
  for (const item of items) if (item.turnId) last.set(item.turnId, item.id);
  const end = (item: Item): TranscriptRow[] => {
    const t = item.turnId ? turns.get(item.turnId) : undefined;
    return t && last.get(t.id) === item.id && (t.status === "interrupted" || t.status === "failed") ? [{ kind: "turn", turn: t }] : [];
  };
  const out: TranscriptRow[] = [];
  for (const s of grouping ? segments(items, turns) : items.map((item) => ({ item }))) {
    if ("item" in s) {
      out.push({ kind: "item", item: s.item }, ...end(s.item));
      continue;
    }
    const open = expanded.get(s.run.id) ?? s.run.live;
    out.push({ kind: "run", run: s.run, expanded: open });
    if (open) out.push(...s.run.items.map((item): TranscriptRow => ({ kind: "item", item })));
    out.push(...end(s.run.items[s.run.items.length - 1]));
  }
  return out;
}

/** The runs that fold, in order. */
export function toolRuns(items: Item[], turns: ReadonlyMap<string, Turn>): ToolRun[] {
  return segments(items, turns).flatMap((s) => ("run" in s ? [s.run] : []));
}

/** The run an item folds into, if it does. */
export function runContaining(itemId: string, items: Item[], turns: ReadonlyMap<string, Turn>): ToolRun | undefined {
  return toolRuns(items, turns).find((r) => r.items.some((it) => it.id === itemId));
}

/** Whether an item can be part of a run: reasoning, and tool calls that
 *  neither wait for approval, failed, nor show images. */
export function folds(item: Item): boolean {
  if (item.kind === "reasoning") return true;
  return item.kind === "tool_call" && item.status !== "pending" && item.status !== "failed" && !item.images?.length;
}

function segments(items: Item[], turns: ReadonlyMap<string, Turn>): Segment[] {
  const lastOfTurn = new Map<string, number>();
  items.forEach((item, i) => lastOfTurn.set(item.turnId ?? "", i));
  const out: Segment[] = [];
  for (let i = 0; i < items.length; ) {
    const first = items[i];
    if (!folds(first)) {
      out.push({ item: first });
      i++;
      continue;
    }
    // The run's last item is j; k looks past the prompts queued meanwhile.
    let j = i;
    for (let k = i; k + 1 < items.length; ) {
      const next = items[k + 1];
      if (next.status === "queued") {
        k++;
      } else if (folds(next) && next.turnId === first.turnId) {
        j = ++k;
      } else {
        break;
      }
    }
    const span = items.slice(i, j + 1);
    const run = span.filter((it) => it.status !== "queued");
    if (run.filter((it) => it.kind === "tool_call").length >= minimumTools) {
      const running = first.turnId !== undefined && turns.get(first.turnId)?.status === "running";
      out.push({ run: { id: "tools:" + first.id, items: run, live: running && lastOfTurn.get(first.turnId ?? "") === j } });
      out.push(...span.filter((it) => it.status === "queued").map((item) => ({ item })));
    } else {
      out.push(...span.map((item) => ({ item })));
    }
    i = j + 1;
  }
  return out;
}

/** The run's tool calls, without the reasoning. */
export function runTools(run: ToolRun): Item[] {
  return run.items.filter((it) => it.kind === "tool_call");
}

/** The agent is at work in the run. */
export function runWorking(run: ToolRun): boolean {
  return run.live || run.items.some((it) => it.status === "in_progress");
}

/** The order the kinds are summed up in. */
const kinds: ToolKind[] = ["execute", "edit", "read", "search", "fetch", "think", "other"];

/** A tool call's kind, "other" for one this client doesn't know. */
function kindOf(item: Item): ToolKind {
  const k = item.tool?.kind;
  return k !== undefined && kinds.includes(k) ? k : "other";
}

/** The calls of each kind: files edited and read count once each, by their
 *  paths (a call without paths is a file of its own); the rest count by
 *  calls. */
export function runParts(tools: Item[]): ToolRunPart[] {
  return kinds.flatMap((kind) => {
    const calls = tools.filter((it) => kindOf(it) === kind);
    if (calls.length === 0) return [];
    let count = calls.length;
    if (kind === "edit" || kind === "read") {
      const files = new Set<string>();
      for (const c of calls) {
        const paths = c.tool?.paths ?? [];
        if (paths.length === 0) files.add("\0" + c.id);
        else paths.forEach((p) => files.add(p));
      }
      count = files.size;
    }
    return [{ kind, count, text: phrase(kind, count) }];
  });
}

function phrase(kind: ToolKind, n: number): string {
  const of = (one: string, many: string) => `${n} ${n === 1 ? one : many}`;
  switch (kind) {
    case "execute":
      return "ran " + of("command", "commands");
    case "edit":
      return "edited " + of("file", "files");
    case "read":
      return "read " + of("file", "files");
    case "search":
      return "searched " + of("time", "times");
    case "fetch":
      return "fetched " + of("page", "pages");
    case "think":
      return "started " + of("agent", "agents");
    default:
      return "used " + of("tool", "tools");
  }
}

/** The parts as a sentence: "Ran 4 commands, edited 3 files". */
export function runTitle(parts: ToolRunPart[]): string {
  return capitalized(parts.map((p) => p.text).join(", "));
}

/** The text with its first letter in capitals, as a part that starts the summary. */
export function capitalized(text: string): string {
  return text.slice(0, 1).toUpperCase() + text.slice(1);
}
