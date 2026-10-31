// The occurrences of what find looks for in the transcript's text, which the
// CSS Custom Highlight API marks without touching the DOM. Where a browser
// lacks it, the matching items' tints show alone.

/** An occurrence in text split into runs (a text node's each): the run and
 *  the offset in it where it starts, and where it ends. It can span runs. */
export interface Occurrence {
  start: [run: number, offset: number];
  end: [run: number, offset: number];
}

/** Where the query, trimmed, is in the runs' text, case-insensitively. */
export function occurrences(runs: string[], query: string): Occurrence[] {
  const q = query.trim();
  if (q === "") return [];
  const starts: number[] = [];
  let text = "";
  for (const r of runs) {
    starts.push(text.length);
    text += r;
  }
  // The run holding the character at `pos`, looking on from `from`.
  const runAt = (pos: number, from: number) => {
    let i = from;
    while (i < runs.length - 1 && pos >= starts[i] + runs[i].length) i++;
    return i;
  };
  const out: Occurrence[] = [];
  let run = 0;
  for (const m of text.matchAll(new RegExp(escapeRegExp(q), "giu"))) {
    const from = m.index;
    const to = from + m[0].length;
    run = runAt(from, run);
    const last = runAt(to - 1, run);
    out.push({ start: [run, from - starts[run]], end: [last, to - starts[last]] });
  }
  return out;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** Ranges over the occurrences of the query in an element's text. */
export function occurrenceRanges(root: Element, query: string): Range[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => (n.parentElement?.tagName === "TEXTAREA" ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT),
  });
  const nodes: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) nodes.push(n as Text);
  return occurrences(
    nodes.map((n) => n.data),
    query,
  ).map(({ start, end }) => {
    const r = new Range();
    r.setStart(nodes[start[0]], start[1]);
    r.setEnd(nodes[end[0]], end[1]);
    return r;
  });
}

/** The page's highlights, where the browser has the Custom Highlight API. */
export function highlightRegistry(): HighlightRegistry | undefined {
  return typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight === "function" ? CSS.highlights : undefined;
}
