// Mentions of other sessions in a prompt: typing `@` lists the server's
// sessions, and picking one writes `@[title](session:<id>)`, which rad tells
// the agent to read with its read_session tool. Transcripts show a mention
// as `@title`. A port of gpui's store/mentions.rs; every client's tests share
// protocol/vectors/session_mentions.json.

/** The most sessions the picker lists. */
export const MAX_MENTION_SUGGESTIONS = 8;
/** A longer query is prose, not a session's name. */
const MAX_QUERY = 50;

const space = /^\s/u;
const alphanumeric = /^[\p{Alphabetic}\p{N}]$/u;

/** An `@` being typed: where it starts (a string index) and what follows
 *  it, up to the cursor. */
export interface MentionQuery {
  start: number;
  query: string;
}

/** The `@` before the cursor, on its line, at the start or after a space,
 *  with no space right after it and not yet a mention. */
export function mentionQuery(text: string, cursor: number): MentionQuery | undefined {
  if (cursor < 0 || cursor > text.length) return undefined;
  const before = text.slice(0, cursor);
  const line = before.lastIndexOf("\n") + 1;
  const at = before.lastIndexOf("@");
  if (at < line) return undefined;
  if (at > 0 && !space.test(before[at - 1])) return undefined;
  const query = before.slice(at + 1);
  if (space.test(query) || query.startsWith("[") || [...query].length > MAX_QUERY) return undefined;
  return { start: at, query };
}

/** The mention of a session: its title, on one line and without brackets. */
export function mentionText(title: string, id: string): string {
  const t = title.replace(/\[/g, "(").replace(/\]/g, ")").replace(/\s/gu, " ").trim();
  return `@[${t === "" ? "Untitled" : t}](session:${id})`;
}

/** The text with the query replaced by the mention and a space, and the
 *  cursor after that space. */
export function completeMention(text: string, cursor: number, q: MentionQuery, title: string, id: string): { text: string; cursor: number } {
  const mention = mentionText(title, id);
  const after = text.slice(cursor);
  const sep = space.test(after) ? "" : " ";
  return { text: text.slice(0, q.start) + mention + sep + after, cursor: q.start + mention.length + 1 };
}

/** A session the picker can list. */
export interface MentionCandidate {
  id: string;
  title: string;
  updatedAt: string;
}

/** The sessions that match a query, best first: the title starts with it,
 *  a word of the title does, or the title holds it; then the latest. All
 *  but the session itself, archived ones too. */
export function mentionSuggestions<T extends MentionCandidate>(query: string, sessions: readonly T[], selfId: string): T[] {
  const q = query.trim().toLowerCase();
  const ranked: { rank: number; index: number; session: T }[] = [];
  sessions.forEach((session, index) => {
    if (session.id === selfId) return;
    const r = rank(session.title.toLowerCase(), q);
    if (r !== undefined) ranked.push({ rank: r, index, session });
  });
  ranked.sort(
    (a, b) =>
      a.rank - b.rank ||
      (a.session.updatedAt < b.session.updatedAt ? 1 : a.session.updatedAt > b.session.updatedAt ? -1 : 0) ||
      a.index - b.index,
  );
  return ranked.slice(0, MAX_MENTION_SUGGESTIONS).map((r) => r.session);
}

function rank(title: string, q: string): number | undefined {
  if (q === "" || title.startsWith(q)) return 0;
  let prev: string | undefined;
  let contains = false;
  for (let i = 0; i < title.length; ) {
    const c = String.fromCodePoint(title.codePointAt(i)!);
    if (title.startsWith(q, i)) {
      if (prev !== undefined && !alphanumeric.test(prev)) return 1;
      contains = true;
    }
    prev = c;
    i += c.length;
  }
  return contains ? 2 : undefined;
}

/** A mention in a text: its range [start, end), title and session id. */
export interface Mention {
  start: number;
  end: number;
  title: string;
  id: string;
}

const tag = "](session:";

/** The mentions in a text, in order. */
export function mentions(text: string): Mention[] {
  const out: Mention[] = [];
  let from = 0;
  for (;;) {
    const start = text.indexOf("@[", from);
    if (start < 0) break;
    from = start + 2;
    const rest = text.slice(from);
    const close = rest.search(/[[\]\n]/);
    if (close < 0) break;
    if (!rest.startsWith(tag, close)) continue;
    const idFrom = close + tag.length;
    let idLen = rest.slice(idFrom).search(/[^A-Za-z0-9-]/);
    if (idLen < 0) idLen = rest.length - idFrom;
    if (idLen === 0 || rest[idFrom + idLen] !== ")") continue;
    const end = from + idFrom + idLen + 1;
    out.push({ start, end, title: rest.slice(0, close), id: rest.slice(idFrom, idFrom + idLen) });
    from = end;
  }
  return out;
}

/** A text as transcripts show it: each mention as `@title`. */
export function mentionDisplay(text: string): string {
  const list = mentions(text);
  if (list.length === 0) return text;
  let out = "";
  let last = 0;
  for (const m of list) {
    out += text.slice(last, m.start) + "@" + m.title;
    last = m.end;
  }
  return out + text.slice(last);
}
