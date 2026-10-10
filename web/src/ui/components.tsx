import {
  Braces,
  Circle,
  CircleStop,
  CodeXml,
  Cpu,
  Diamond,
  Drama,
  Hand,
  MousePointerClick,
  Pi,
  Sparkle,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import { parseDate, type SessionStatus } from "../protocol/models";
import type { ConnectionState } from "../store/serverConnection";
import { Spinner } from "./primitives";

export function StatusIcon({ status }: { status: SessionStatus }) {
  let icon;
  switch (status) {
    case "running":
      icon = <Spinner size={15} />;
      break;
    case "awaiting_approval":
      icon = <Hand size={15} strokeWidth={2.25} className="c-yellow" />;
      break;
    case "error":
      icon = <TriangleAlert size={15} className="c-red" />;
      break;
    case "stopped":
      icon = <CircleStop size={15} className="c-subtext" />;
      break;
    default:
      icon = <Circle size={15} className="c-overlay" />;
  }
  return (
    <span className="status-icon" title={status.replace("_", " ")}>
      {icon}
    </span>
  );
}

const harnessIcons: Record<string, [LucideIcon, string]> = {
  claude: [Sparkle, "c-peach"],
  codex: [CodeXml, "c-text"],
  fake: [Drama, "c-subtext"],
  "acp:cursor": [MousePointerClick, "c-subtext"],
  "acp:opencode": [Braces, "c-subtext"],
  "acp:gemini": [Diamond, "c-blue"],
  pi: [Pi, "c-subtext"],
  "acp:omp": [Pi, "c-subtext"],
};

export function HarnessIcon({ id, size = 14 }: { id: string; size?: number }) {
  const [Icon, color] = harnessIcons[id] ?? [Cpu, "c-subtext"];
  return <Icon size={size} className={"harness-icon " + color} aria-hidden />;
}

export function HarnessBadge({ id, name }: { id: string; name?: string }) {
  return (
    <span className="harness-badge">
      <HarnessIcon id={id} size={12} />
      {name ?? id}
    </span>
  );
}

/** A small ring and percentage showing how full the agent's context is. */
export function ContextGauge({ fraction }: { fraction: number }) {
  const color = fraction >= 0.9 ? "c-red" : fraction >= 0.7 ? "c-yellow" : "c-subtext";
  const percent = Math.round(fraction * 100) + "%";
  const r = 4;
  const c = 2 * Math.PI * r;
  return (
    <span className={"context-gauge " + color} title={`${percent} of context used`}>
      <svg width="11" height="11" viewBox="0 0 11 11" aria-hidden>
        <circle cx="5.5" cy="5.5" r={r} fill="none" stroke="var(--surface1)" strokeWidth="2" />
        <circle
          cx="5.5"
          cy="5.5"
          r={r}
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeDasharray={`${c * fraction} ${c}`}
          transform="rotate(-90 5.5 5.5)"
        />
      </svg>
      {percent}
    </span>
  );
}

export function connectionLabel(s: ConnectionState): string {
  switch (s.kind) {
    case "connected":
      return "Connected";
    case "connecting":
      return "Connecting";
    case "failed":
      return "Failed: " + s.message;
    default:
      return "Not connected";
  }
}

export function ConnectionDot({ state }: { state: ConnectionState }) {
  const color = { connected: "green", connecting: "yellow", failed: "red", idle: "overlay" }[state.kind];
  return <span className="connection-dot" style={{ background: `var(--${color})` }} title={connectionLabel(state)} role="img" aria-label={connectionLabel(state)} />;
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 86400],
  ["month", 30 * 86400],
  ["week", 7 * 86400],
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
];

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "narrow" });

export function relativeTime(ms: number, now = Date.now()): string {
  const s = (ms - now) / 1000;
  for (const [unit, size] of units) {
    if (Math.abs(s) >= size) return relative.format(Math.round(s / size), unit);
  }
  return relative.format(0, "second");
}

/** "5 min. ago", kept current. */
export function RelativeTime({ date }: { date: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(t);
  }, []);
  const ms = parseDate(date);
  return (
    <time className="relative-time" dateTime={date} title={new Date(ms).toLocaleString()}>
      {relativeTime(ms)}
    </time>
  );
}
