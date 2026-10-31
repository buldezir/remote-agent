// Usage limits: the gauge the clients show for an agent's plan ("5h 34% ·
// week 61%"), its colour, the details behind it, and the notice for a turn
// that hit a limit. A port of ra's server/internal/client/limits.go, as
// RAKit's UsageLimitsFormat and android/core's are; their tests share
// protocol/vectors/usage_limits.json.
import { parseDate, type UsageLimits, type UsageWindow } from "../protocol/models";

/** How near a limit is, for the gauge's colour: amber from `WARNING_PERCENT`,
 *  red once spent. */
export type LimitLevel = "normal" | "warning" | "limited";

/** The use from which a window shows amber. */
export const WARNING_PERCENT = 80;

const minute = 60_000;

/** A time the server sent, in ms; undefined when absent or unreadable. */
function time(s: string | undefined): number | undefined {
  const t = parseDate(s);
  return Number.isNaN(t) ? undefined : t;
}

/** The window has reset since the agent said. */
function hasReset(w: UsageWindow, now: number): boolean {
  const r = time(w.resetsAt);
  return r !== undefined && r <= now;
}

/** A window's use now: 0 once it has reset since the agent said. */
export function windowPercent(w: UsageWindow, now: number): number {
  if (hasReset(w, now)) return 0;
  return Math.max(0, Math.min(Math.round(w.usedPercent), 100));
}

function percentLevel(p: number): LimitLevel {
  if (p >= 100) return "limited";
  if (p >= WARNING_PERCENT) return "warning";
  return "normal";
}

/** A window's colour now. */
export function windowLevel(w: UsageWindow, now: number): LimitLevel {
  return percentLevel(windowPercent(w, now));
}

/** The limit the agent hit hasn't reset yet. */
export function limitedNow(l: UsageLimits, now: number): boolean {
  if (!l.limited) return false;
  const r = time(l.resetsAt);
  return r === undefined || r > now;
}

/** The gauge's colour: its most used window's, or red while a limit holds. */
export function limitsLevel(l: UsageLimits, now: number): LimitLevel {
  if (limitedNow(l, now)) return "limited";
  let level: LimitLevel = "normal";
  for (const w of l.windows ?? []) {
    switch (windowLevel(w, now)) {
      case "limited":
        return "limited";
      case "warning":
        level = "warning";
    }
  }
  return level;
}

/** The gauge: each window's label and use, "5h 34% · week 61%"; "" with
 *  nothing to show. */
export function limitsGauge(l: UsageLimits, now: number): string {
  const parts = (l.windows ?? []).map((w) => `${w.label} ${windowPercent(w, now)}%`);
  if (parts.length === 0 && limitedNow(l, now)) return "Limit reached";
  return parts.join(" · ");
}

/** The lines behind the gauge, on hover or a tap: the limit hit, each
 *  window's use and reset, extra usage, the plan, and how old they are. */
export function limitsDetails(l: UsageLimits, now: number): string[] {
  const lines: string[] = [];
  if (limitedNow(l, now)) lines.push("Limit reached" + resetsIn(l.resetsAt, now));
  for (const w of l.windows ?? []) {
    if (hasReset(w, now)) {
      lines.push(`${w.label}: reset ${limitDuration(now - time(w.resetsAt)!, false)} ago`);
      continue;
    }
    lines.push(`${w.label}: ${windowPercent(w, now)}% used${resetsIn(w.resetsAt, now)}`);
  }
  if (l.overage) lines.push("Using extra usage");
  if (l.plan) lines.push(l.plan + " plan");
  const age = now - (time(l.updatedAt) ?? now);
  lines.push(age < minute ? "Updated just now" : `Updated ${limitDuration(age, false)} ago`);
  return lines;
}

function resetsIn(at: string | undefined, now: number): string {
  const t = time(at);
  if (t === undefined || t <= now) return "";
  return " · resets in " + limitDuration(t - now, true);
}

/** What the session shows for an error whose turn hit the limit `w`:
 *  "5h limit reached · resets in 2h 14m". */
export function limitNotice(w: UsageWindow, now: number): string {
  let s = w.label + " limit reached";
  const t = time(w.resetsAt);
  if (t !== undefined) {
    if (t > now) s += " · resets in " + limitDuration(t - now, true);
    else s += " · reset " + limitDuration(now - t, false) + " ago";
  }
  return s.replace(/^./u, (c) => c.toUpperCase());
}

/** A span in ms to a reset, or since one: "45m", "2h 14m", "3d 4h". A span
 *  to come rounds its minutes up, one past down; either is a minute at
 *  least. */
export function limitDuration(ms: number, future: boolean): string {
  let m = Math.trunc(ms / minute);
  if (future && ms % minute !== 0) m++;
  m = Math.max(m, 1);
  if (m < 60) return `${m}m`;
  if (m < 24 * 60) return m % 60 === 0 ? `${m / 60}h` : `${Math.floor(m / 60)}h ${m % 60}m`;
  const days = Math.floor(m / (24 * 60));
  const hours = Math.floor((m % (24 * 60)) / 60);
  return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
}
