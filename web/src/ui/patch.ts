export type DiffLineKind = "add" | "del" | "hunk" | "meta" | "context";

export interface DiffLine {
  text: string;
  kind: DiffLineKind;
}

/** Splits a multi-file unified diff into per-file sections keyed by new path. */
export function splitPatch(patch: string): Map<string, string> {
  const out = new Map<string, string>();
  let current: string[] = [];
  const flush = () => {
    const header = current[0];
    if (header === undefined) return;
    let path: string | undefined;
    for (const l of current.slice(0, 8)) {
      if (l.startsWith("+++ b/")) path = l.slice(6);
      else if (l.startsWith("rename to ")) path = l.slice(10);
      else if (l.startsWith("--- a/") && path === undefined) path = l.slice(6);
    }
    if (path === undefined) {
      const i = header.indexOf(" b/");
      if (i >= 0) path = header.slice(i + 3);
    }
    if (path !== undefined) out.set(path, current.join("\n"));
  };
  for (const line of patch.split("\n")) {
    if (line.startsWith("diff --git ")) {
      flush();
      current = [line];
    } else if (current.length) {
      current.push(line);
    }
  }
  flush();
  return out;
}

/** A file's section as lines, from its first hunk. */
export function patchLines(section: string): DiffLine[] {
  const out: DiffLine[] = [];
  let inHunk = false;
  for (const text of section.split("\n")) {
    let kind: DiffLineKind;
    if (text.startsWith("@@")) {
      kind = "hunk";
      inHunk = true;
    } else if (!inHunk) continue; // diff/index/---/+++ headers
    else if (text.startsWith("+")) kind = "add";
    else if (text.startsWith("-")) kind = "del";
    else if (text.startsWith("\\")) kind = "meta";
    else kind = "context";
    out.push({ text, kind });
  }
  if (out.length && out[out.length - 1].text === "") out.pop();
  return out;
}
