import type { SearchResult, SearchResults, ServerInfo } from "../protocol/models";
import { Observable } from "./observable";

/** What a search needs from a server's connection; `ServerConnection` is one. */
export interface SearchSource {
  readonly server: { readonly id: string; readonly name: string };
  readonly isConnected: boolean;
  readonly info: ServerInfo | undefined;
  search(query: string, archived: boolean, limit?: number): Promise<SearchResults>;
}

/** A session one of the servers found. */
export interface SearchHit {
  /** "<server id>/<session id>". */
  id: string;
  serverId: string;
  result: SearchResult;
}

/** A server whose search failed, and why. */
export interface SearchFailure {
  server: string;
  message: string;
}

/** The results of every server, one list each in the app's server order,
 *  merged by rank: every server's first, then every server's second, and
 *  so on, each server's own order kept. Scores aren't comparable across
 *  servers, so this is the usual way to merge a federated search. */
export function mergeSearchResults<T>(lists: T[][]): T[] {
  const out: T[] = [];
  for (let rank = 0; ; rank++) {
    let any = false;
    for (const list of lists) {
      if (rank < list.length) {
        out.push(list[rank]);
        any = true;
      }
    }
    if (!any) return out;
  }
}

/** What one server answered to the current search. */
type Answer = { kind: "results"; results: SearchResults } | { kind: "failed"; message: string };

/** Search across every server connected: the sessions whose prompts,
 *  replies and other items have the query, or whose title or branch do. It
 *  searches once the query has `minLength` characters, `debounce` ms after
 *  the last change, asking each connected server that searches (its info
 *  says `search`) at once and merging their results as they come. An answer
 *  to an earlier query is dropped. As RAKit's SessionSearch. */
export class SessionSearch extends Observable {
  static readonly minLength = 2;
  /** Sessions each server returns at most. */
  static readonly limit = 30;

  query = "";
  /** Archived sessions too; kept while the app runs. */
  includeArchived = true;
  hits: SearchHit[] = [];
  /** A server hasn't answered the current search yet, or it waits out the debounce. */
  isSearching = false;
  failures: SearchFailure[] = [];
  /** A server found more sessions than it returned. */
  more = false;
  /** The servers still indexing sessions an older rad wrote, by name. */
  indexing: string[] = [];
  /** The servers connected whose rad doesn't search, by name. */
  unsupported: string[] = [];
  /** Milliseconds after the last change before it searches. */
  debounce = 250;

  private generation = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  /** The servers the last search asked, in order. */
  private asked: SearchSource[] = [];
  /** `serversKey` when the last search started. */
  private askedKey = "";
  private answers = new Map<string, Answer>();

  constructor(private sources: () => SearchSource[]) {
    super();
  }

  /** The query is long enough to search: its results show in place of the usual rows. */
  get isActive(): boolean {
    return this.query.trim().length >= SessionSearch.minLength;
  }

  setQuery(query: string) {
    if (query === this.query) return;
    this.query = query;
    this.schedule();
  }

  setIncludeArchived(on: boolean) {
    if (on === this.includeArchived) return;
    this.includeArchived = on;
    this.schedule();
  }

  /** No server could be searched: none is connected, or none that searches. */
  get noServers(): boolean {
    return this.isActive && !this.isSearching && this.asked.length === 0 && this.unsupported.length === 0;
  }

  /** How many servers answered the current search, failures aside. */
  get answered(): number {
    let n = 0;
    for (const a of this.answers.values()) if (a.kind === "results") n++;
    return n;
  }

  /** The servers connected now, and which of them search, for a view to
   *  call `serversChanged` when it changes. */
  serversKey(): string {
    return this.sources()
      .filter((s) => s.isConnected)
      .map((s) => (s.info?.search ? s.server.id : "!" + s.server.id))
      .join(" ");
  }

  /** Searches again at once if the servers connected aren't the ones the
   *  last search found, as when one connects or goes. */
  serversChanged() {
    if (!this.isActive || this.timer !== undefined) return;
    if (this.serversKey() !== this.askedKey) void this.run(++this.generation);
  }

  private schedule() {
    const generation = ++this.generation;
    clearTimeout(this.timer);
    this.timer = undefined;
    if (!this.isActive) {
      this.hits = [];
      this.isSearching = false;
      this.failures = [];
      this.more = false;
      this.indexing = [];
      this.unsupported = [];
      this.asked = [];
      this.askedKey = "";
      this.answers.clear();
      this.changed();
      return;
    }
    // The results shown stay until the new ones come.
    this.isSearching = true;
    this.changed();
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.run(generation);
    }, this.debounce);
  }

  private async run(generation: number) {
    const query = this.query.trim();
    const archived = this.includeArchived;
    this.askedKey = this.serversKey();
    const connected = this.sources().filter((s) => s.isConnected);
    this.asked = connected.filter((s) => s.info?.search);
    this.unsupported = connected.filter((s) => !s.info?.search).map((s) => s.server.name);
    this.answers = new Map();
    this.isSearching = this.asked.length > 0;
    if (!this.isSearching) this.collect();
    this.changed();
    await Promise.all(
      this.asked.map(async (source) => {
        let answer: Answer;
        try {
          answer = { kind: "results", results: await source.search(query, archived, SessionSearch.limit) };
        } catch (e) {
          answer = { kind: "failed", message: (e as Error).message };
        }
        if (generation !== this.generation) return;
        this.answers.set(source.server.id, answer);
        this.isSearching = this.answers.size < this.asked.length;
        this.collect();
        this.changed();
      }),
    );
  }

  /** The state from the answers so far, in the servers' order. */
  private collect() {
    const lists: SearchHit[][] = [];
    const failures: SearchFailure[] = [];
    const indexing: string[] = [];
    let more = false;
    for (const source of this.asked) {
      const answer = this.answers.get(source.server.id);
      if (!answer) continue;
      if (answer.kind === "failed") {
        failures.push({ server: source.server.name, message: answer.message });
        continue;
      }
      const serverId = source.server.id;
      lists.push(answer.results.results.map((result) => ({ id: `${serverId}/${result.session.id}`, serverId, result })));
      if (answer.results.more) more = true;
      if (answer.results.indexing) indexing.push(source.server.name);
    }
    this.hits = mergeSearchResults(lists);
    this.failures = failures;
    this.more = more;
    this.indexing = indexing;
  }
}
