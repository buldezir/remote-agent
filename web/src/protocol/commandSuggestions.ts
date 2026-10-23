import type { SlashCommand } from "./models";

/** Only complete the leading command token; arguments and ordinary prose stay untouched. */
export function commandQuery(draft: string): string | undefined {
  return draft.startsWith("/") && !/\s/u.test(draft) ? draft.slice(1).toLowerCase() : undefined;
}

export function commandSuggestions(draft: string, commands: SlashCommand[]): SlashCommand[] {
  const query = commandQuery(draft);
  if (query === undefined) return [];
  const seen = new Set<string>();
  return commands.flatMap((command, index) => {
    if (seen.has(command.name)) return [];
    seen.add(command.name);
    const name = command.name.toLowerCase(), short = name.split(":").at(-1)!;
    const rank = name === query || short === query ? 0
      : name.startsWith(query) || short.startsWith(query) ? 1
      : name.includes(query) ? 2
      : command.description?.toLowerCase().includes(query) ? 3 : undefined;
    return rank === undefined ? [] : [{ command, index, rank }];
  }).sort((a, b) => a.rank - b.rank || a.index - b.index).map((entry) => entry.command);
}
